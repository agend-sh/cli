package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ggrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/agend-sh/cli/internal/api"
	agentgrpc "github.com/agend-sh/cli/internal/grpc"
	pb "github.com/agend-sh/cli/proto/agentd/v1"
)

type authRecoveryTestServer struct {
	pb.UnimplementedAgentServiceServer

	mu                 sync.Mutex
	staleCalls         int
	rotatedSecretCalls int
	rotatedExecCalls   int
	staleRelease       chan struct{}
	staleReleaseOnce   sync.Once
}

func newAuthRecoveryTestServer() *authRecoveryTestServer {
	return &authRecoveryTestServer{staleRelease: make(chan struct{})}
}

func (s *authRecoveryTestServer) interceptor(
	ctx context.Context,
	req any,
	info *ggrpc.UnaryServerInfo,
	handler ggrpc.UnaryHandler,
) (any, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	secret := firstMD(md, "x-one-time-secret")
	token := firstMD(md, "x-session-token")
	switch {
	case token == "stale-session":
		s.mu.Lock()
		s.staleCalls++
		if s.staleCalls == 2 {
			s.staleReleaseOnce.Do(func() { close(s.staleRelease) })
		}
		s.mu.Unlock()
		select {
		case <-s.staleRelease:
		case <-time.After(2 * time.Second):
			return nil, status.Error(codes.DeadlineExceeded, "second stale call did not arrive")
		}
		return nil, status.Error(codes.Unauthenticated, "invalid session token")

	case secret == "rotated-secret":
		s.mu.Lock()
		s.rotatedSecretCalls++
		alreadyConsumed := s.rotatedSecretCalls > 1
		s.mu.Unlock()
		if alreadyConsumed {
			return nil, status.Error(codes.Unauthenticated, "one-time secret already consumed")
		}
		response, err := handler(ctx, req)
		_ = ggrpc.SetTrailer(ctx, metadata.Pairs("x-session-token", "rotated-session"))
		return response, err

	case token == "rotated-session":
		if info.FullMethod == "/agentd.v1.AgentService/Exec" {
			s.mu.Lock()
			s.rotatedExecCalls++
			s.mu.Unlock()
		}
		return handler(ctx, req)

	case info.FullMethod == "/agentd.v1.AgentService/Ping":
		return handler(ctx, req)

	default:
		return nil, status.Error(codes.Unauthenticated, "missing credentials")
	}
}

func firstMD(md metadata.MD, key string) string {
	values := md.Get(key)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func (s *authRecoveryTestServer) Ping(context.Context, *pb.PingRequest) (*pb.PingResponse, error) {
	return &pb.PingResponse{}, nil
}

func (s *authRecoveryTestServer) Exec(context.Context, *pb.ExecRequest) (*pb.ExecResponse, error) {
	return &pb.ExecResponse{Status: "completed"}, nil
}

func TestConcurrentAuthFailuresShareOneRotationAndHandshake(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	testAgent := newAuthRecoveryTestServer()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := ggrpc.NewServer(ggrpc.UnaryInterceptor(testAgent.interceptor))
	pb.RegisterAgentServiceServer(grpcServer, testAgent)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	var reauthCalls atomic.Int32
	controlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/environments/env-concurrent/reauth":
			reauthCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"env_id": "env-concurrent", "secret": "rotated-secret",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v2/environments/env-concurrent":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"env_id": "env-concurrent", "state": "running", "endpoint": listener.Addr().String(),
			})
		default:
			http.Error(w, "unexpected control-plane request", http.StatusNotFound)
		}
	}))
	defer controlPlane.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	staleClient, err := agentgrpc.Dial(ctx, listener.Addr().String(), "", "stale-session")
	if err != nil {
		t.Fatal(err)
	}
	conn := &EnvConn{
		envID: "env-concurrent", state: StateConnected, client: staleClient, generation: 1,
		endpoint: listener.Addr().String(), sessionToken: "stale-session",
		apiClient: api.New(controlPlane.URL, "control-token"),
	}
	t.Cleanup(conn.disconnect)

	start := make(chan struct{})
	results := make(chan struct {
		text string
		err  bool
	}, 2)
	for range 2 {
		go func() {
			<-start
			text, isErr := conn.Execute(ctx, false, func(client *agentgrpc.Client) (string, bool) {
				_, callErr := client.Agent.Exec(ctx, &pb.ExecRequest{Command: "true"})
				if callErr != nil {
					return fmt.Sprintf("exec failed: %v", callErr), true
				}
				return "ok", false
			})
			results <- struct {
				text string
				err  bool
			}{text: text, err: isErr}
		}()
	}
	close(start)
	for range 2 {
		result := <-results
		if result.err || result.text != "[reconnected] ok" {
			t.Fatalf("Execute() = %q, isError=%v", result.text, result.err)
		}
	}

	if reauthCalls.Load() != 1 {
		t.Fatalf("reauth calls = %d, want 1", reauthCalls.Load())
	}
	testAgent.mu.Lock()
	staleCalls := testAgent.staleCalls
	secretCalls := testAgent.rotatedSecretCalls
	execCalls := testAgent.rotatedExecCalls
	testAgent.mu.Unlock()
	if staleCalls != 2 || secretCalls != 1 || execCalls != 2 {
		t.Fatalf("agent calls: stale=%d rotated_secret=%d rotated_exec=%d; want 2,1,2",
			staleCalls, secretCalls, execCalls)
	}
}

func TestLateOldGenerationFailureDoesNotCloseRecoveredClient(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	testAgent := newAuthRecoveryTestServer()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := ggrpc.NewServer(ggrpc.UnaryInterceptor(testAgent.interceptor))
	pb.RegisterAgentServiceServer(grpcServer, testAgent)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	var reauthCalls atomic.Int32
	controlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/environments/env-fenced/reauth":
			reauthCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"env_id": "env-fenced", "secret": "rotated-secret",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v2/environments/env-fenced":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"env_id": "env-fenced", "state": "running", "endpoint": listener.Addr().String(),
			})
		default:
			http.Error(w, "unexpected control-plane request", http.StatusNotFound)
		}
	}))
	defer controlPlane.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	staleClient, err := agentgrpc.Dial(ctx, listener.Addr().String(), "", "stale-session")
	if err != nil {
		t.Fatal(err)
	}
	conn := &EnvConn{
		envID: "env-fenced", state: StateConnected, client: staleClient, generation: 1,
		endpoint: listener.Addr().String(), sessionToken: "stale-session",
		apiClient: api.New(controlPlane.URL, "control-token"),
	}
	t.Cleanup(conn.disconnect)

	lateEntered := make(chan struct{})
	replacementUsed := make(chan struct{})
	lateResult := make(chan struct {
		text string
		err  bool
	}, 1)
	go func() {
		text, isErr := conn.Execute(ctx, false, func(client *agentgrpc.Client) (string, bool) {
			if client != staleClient {
				return "late mutation unexpectedly replayed", true
			}
			close(lateEntered)
			select {
			case <-replacementUsed:
				return "exec failed: rpc error: code = Unavailable desc = old transport closed", true
			case <-ctx.Done():
				return ctx.Err().Error(), true
			}
		})
		lateResult <- struct {
			text string
			err  bool
		}{text: text, err: isErr}
	}()
	select {
	case <-lateEntered:
	case <-ctx.Done():
		t.Fatal("late old-generation call did not start")
	}

	authText, authErr := conn.Execute(ctx, false, func(client *agentgrpc.Client) (string, bool) {
		if client == staleClient {
			return "exec failed: rpc error: code = Unauthenticated desc = invalid session token", true
		}
		close(replacementUsed)
		return "ok", false
	})
	if authErr || authText != "[reconnected] ok" {
		t.Fatalf("auth recovery = %q, isError=%v", authText, authErr)
	}

	late := <-lateResult
	if !late.err || late.text != "exec failed: rpc error: code = Unavailable desc = old transport closed" {
		t.Fatalf("late result = %q, isError=%v", late.text, late.err)
	}
	if reauthCalls.Load() != 1 {
		t.Fatalf("reauth calls = %d, want 1", reauthCalls.Load())
	}

	conn.mu.Lock()
	current := conn.client
	state := conn.state
	conn.mu.Unlock()
	if current == nil || current == staleClient || state != StateConnected {
		t.Fatalf("replacement was lost: client=%p stale=%p state=%v", current, staleClient, state)
	}
	if _, err := current.Agent.Exec(ctx, &pb.ExecRequest{Command: "still-connected"}); err != nil {
		t.Fatalf("replacement client was closed by late failure: %v", err)
	}
}

func TestRecoveryGateAndBackoffHonorCancellation(t *testing.T) {
	conn := &EnvConn{}
	if err := conn.acquireRecovery(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer conn.releaseRecovery()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if err := conn.acquireRecovery(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("recovery gate error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("cancelled recovery gate wait took %s", elapsed)
	}

	started = time.Now()
	if err := waitForContext(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("backoff error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("cancelled backoff took %s", elapsed)
	}
}

func TestSemanticErrorDoesNotReconnectOrRetry(t *testing.T) {
	for _, original := range []string{
		"task_output failed: rpc error: code = NotFound desc = task not found: task_1",
		"download failed: local_path /tmp/escape is outside the allowed directory /tmp/root (set AGEND_LOCAL_ROOT to change it)",
	} {
		t.Run(original, func(t *testing.T) {
			client := &agentgrpc.Client{}
			conn := &EnvConn{
				envID: "env-semantic-error", state: StateConnected,
				client: client, generation: 1,
			}
			calls := 0
			started := time.Now()
			text, isErr := conn.Execute(context.Background(), true, func(got *agentgrpc.Client) (string, bool) {
				calls++
				if got != client {
					t.Fatalf("tool call used client %p, want %p", got, client)
				}
				return original, true
			})
			if !isErr || text != original {
				t.Fatalf("Execute() = %q, isError=%v; want original semantic error", text, isErr)
			}
			if calls != 1 {
				t.Fatalf("tool calls = %d, want exactly 1", calls)
			}
			if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
				t.Fatalf("semantic error recovery took %s; want immediate return", elapsed)
			}
			conn.mu.Lock()
			defer conn.mu.Unlock()
			if conn.state != StateConnected || conn.client != client || conn.generation != 1 {
				t.Fatalf("semantic error changed connection: state=%v client=%p generation=%d",
					conn.state, conn.client, conn.generation)
			}
		})
	}
}

func TestCancelledCallDoesNotWaitForRecoveryStateMutex(t *testing.T) {
	conn := &EnvConn{}
	if err := conn.acquireRecovery(context.Background()); err != nil {
		t.Fatal(err)
	}
	conn.mu.Lock()
	defer func() {
		conn.mu.Unlock()
		conn.releaseRecovery()
	}()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	executeDone := make(chan struct {
		text string
		err  bool
	}, 1)
	go func() {
		text, isErr := conn.Execute(ctx, false, func(*agentgrpc.Client) (string, bool) {
			panic("cancelled call dispatched")
		})
		executeDone <- struct {
			text string
			err  bool
		}{text: text, err: isErr}
	}()
	select {
	case result := <-executeDone:
		if !result.err || !strings.Contains(result.text, context.Canceled.Error()) {
			t.Fatalf("Execute = %q, isError=%v; want context cancellation", result.text, result.err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancelled Execute blocked on recovery's state mutex")
	}

	invokeDone := make(chan error, 1)
	go func() {
		_, _, _, _, _, err := conn.invokeCurrent(ctx, func(*agentgrpc.Client) (string, bool) {
			panic("cancelled invocation dispatched")
		})
		invokeDone <- err
	}()
	select {
	case err := <-invokeDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("invokeCurrent = %v, want context.Canceled", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancelled invocation blocked on recovery's state mutex")
	}
}

func TestCancelledCallLeavesWhileRecoveryHTTPIsBlocked(t *testing.T) {
	entered := make(chan struct{})
	var enterOnce sync.Once
	controlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enterOnce.Do(func() { close(entered) })
		<-r.Context().Done()
	}))
	defer controlPlane.Close()

	conn := &EnvConn{
		envID:     "env-slow-recovery",
		state:     StateDisconnected,
		apiClient: api.New(controlPlane.URL, "control-token"),
	}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	firstDone := make(chan error, 1)
	go func() { firstDone <- conn.EnsureConnected(firstCtx) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		cancelFirst()
		t.Fatal("recovery did not enter the control-plane request")
	}

	cancelledCtx, cancelSecond := context.WithCancel(context.Background())
	cancelSecond()
	started := time.Now()
	text, isErr := conn.Execute(cancelledCtx, false, func(*agentgrpc.Client) (string, bool) {
		panic("cancelled call dispatched")
	})
	if !isErr || !strings.Contains(text, context.Canceled.Error()) {
		cancelFirst()
		t.Fatalf("Execute = %q, isError=%v; want context cancellation", text, isErr)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		cancelFirst()
		t.Fatalf("cancelled call waited %s behind recovery HTTP", elapsed)
	}

	cancelFirst()
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked recovery error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("blocked recovery ignored cancellation")
	}
}

func TestAmbiguousReauthResponseIsFencedOncePerCredentialGeneration(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	testAgent := newAuthRecoveryTestServer()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := ggrpc.NewServer(ggrpc.UnaryInterceptor(testAgent.interceptor))
	pb.RegisterAgentServiceServer(grpcServer, testAgent)
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(func() {
		grpcServer.Stop()
		_ = listener.Close()
	})

	var reauthCalls atomic.Int32
	controlPlane := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/environments/env-ambiguous/reauth":
			reauthCalls.Add(1)
			// An HTTP failure is ambiguous with respect to the control-plane
			// mutation: it may have committed before this response was produced.
			http.Error(w, `{"error":"response lost after commit"}`, http.StatusBadGateway)
		case r.Method == http.MethodGet && r.URL.Path == "/v2/environments/env-ambiguous":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"env_id": "env-ambiguous", "state": "running", "endpoint": listener.Addr().String(),
			})
		default:
			http.Error(w, "unexpected control-plane request", http.StatusNotFound)
		}
	}))
	defer controlPlane.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	staleClient, err := agentgrpc.Dial(ctx, listener.Addr().String(), "", "stale-session")
	if err != nil {
		t.Fatal(err)
	}
	conn := &EnvConn{
		envID: "env-ambiguous", state: StateConnected, client: staleClient, generation: 1,
		endpoint: listener.Addr().String(), sessionToken: "stale-session",
		apiClient: api.New(controlPlane.URL, "control-token"),
	}
	t.Cleanup(conn.disconnect)

	start := make(chan struct{})
	results := make(chan bool, 2)
	for range 2 {
		go func() {
			<-start
			_, isErr := conn.Execute(ctx, false, func(client *agentgrpc.Client) (string, bool) {
				_, callErr := client.Agent.Exec(ctx, &pb.ExecRequest{Command: "true"})
				if callErr != nil {
					return fmt.Sprintf("exec failed: %v", callErr), true
				}
				return "unexpected success", false
			})
			results <- isErr
		}()
	}
	close(start)
	for range 2 {
		if !<-results {
			t.Fatal("stale credential unexpectedly succeeded")
		}
	}
	if calls := reauthCalls.Load(); calls != 1 {
		t.Fatalf("reauth calls = %d, want exactly 1 for the failed credential generation", calls)
	}
}
