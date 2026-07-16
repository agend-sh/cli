package grpc

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ggrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "github.com/agend-sh/cli/proto/agentd/v1"
)

type concurrentAuthTestServer struct {
	pb.UnimplementedAgentServiceServer

	mu               sync.Mutex
	secretConsumed   bool
	secretCalls      int
	sessionExecs     int
	secondSecret     chan struct{}
	secondSecretOnce sync.Once
}

func newConcurrentAuthTestServer() *concurrentAuthTestServer {
	return &concurrentAuthTestServer{secondSecret: make(chan struct{})}
}

func (s *concurrentAuthTestServer) authInterceptor(
	ctx context.Context,
	req any,
	info *ggrpc.UnaryServerInfo,
	handler ggrpc.UnaryHandler,
) (any, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	secret := firstMetadataValue(md, "x-one-time-secret")
	token := firstMetadataValue(md, "x-session-token")
	if secret != "" {
		s.mu.Lock()
		s.secretCalls++
		call := s.secretCalls
		alreadyConsumed := s.secretConsumed
		if secret == "first-secret" && !alreadyConsumed {
			s.secretConsumed = true
		}
		s.mu.Unlock()

		if call > 1 {
			s.secondSecretOnce.Do(func() { close(s.secondSecret) })
		}
		if secret != "first-secret" || alreadyConsumed {
			if info.FullMethod == "/agentd.v1.AgentService/Ping" {
				// Match the daemon: Ping remains available for health checks even
				// when its supplied credentials are invalid, but no token is issued.
				return handler(ctx, req)
			}
			return nil, status.Error(codes.Unauthenticated, "one-time secret already consumed")
		}
		// Give a broken client enough time to dispatch a second RPC with the
		// same secret. The fixed client holds that peer behind its auth gate.
		select {
		case <-s.secondSecret:
		case <-time.After(100 * time.Millisecond):
		}
		response, err := handler(ctx, req)
		_ = ggrpc.SetTrailer(ctx, metadata.Pairs("x-session-token", "shared-session-token"))
		return response, err
	}
	if token == "shared-session-token" {
		if info.FullMethod == "/agentd.v1.AgentService/Exec" {
			s.mu.Lock()
			s.sessionExecs++
			s.mu.Unlock()
		}
		return handler(ctx, req)
	}
	if info.FullMethod == "/agentd.v1.AgentService/Ping" {
		return handler(ctx, req)
	}
	return nil, status.Error(codes.Unauthenticated, "missing credentials")
}

func firstMetadataValue(md metadata.MD, key string) string {
	values := md.Get(key)
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func (s *concurrentAuthTestServer) Ping(context.Context, *pb.PingRequest) (*pb.PingResponse, error) {
	return &pb.PingResponse{}, nil
}

func (s *concurrentAuthTestServer) Exec(context.Context, *pb.ExecRequest) (*pb.ExecResponse, error) {
	return &pb.ExecResponse{Status: "completed"}, nil
}

func TestConcurrentFirstCallsConsumeSecretOnce(t *testing.T) {
	testServer := newConcurrentAuthTestServer()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := ggrpc.NewServer(ggrpc.UnaryInterceptor(testServer.authInterceptor))
	pb.RegisterAgentServiceServer(server, testServer)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Dial(ctx, listener.Addr().String(), "first-secret", "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var callbacks atomic.Int32
	client.OnTokenReceived = func(token string) {
		if token != "shared-session-token" {
			t.Errorf("session token = %q", token)
		}
		callbacks.Add(1)
	}

	start := make(chan struct{})
	errors := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, callErr := client.Agent.Exec(ctx, &pb.ExecRequest{Command: "true"})
			errors <- callErr
		}()
	}
	close(start)
	for range 2 {
		if callErr := <-errors; callErr != nil {
			t.Fatalf("concurrent Exec: %v", callErr)
		}
	}

	testServer.mu.Lock()
	secretCalls, sessionExecs := testServer.secretCalls, testServer.sessionExecs
	testServer.mu.Unlock()
	if secretCalls != 1 {
		t.Fatalf("one-time-secret calls = %d, want 1", secretCalls)
	}
	if sessionExecs != 2 {
		t.Fatalf("session-authenticated Exec calls = %d, want 2", sessionExecs)
	}
	if callbacks.Load() != 1 {
		t.Fatalf("token callbacks = %d, want 1", callbacks.Load())
	}
}

func TestRejectedHandshakeDoesNotDispatchRequestedMutation(t *testing.T) {
	testServer := newConcurrentAuthTestServer()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := ggrpc.NewServer(ggrpc.UnaryInterceptor(testServer.authInterceptor))
	pb.RegisterAgentServiceServer(server, testServer)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := Dial(ctx, listener.Addr().String(), "wrong-secret", "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Agent.Exec(ctx, &pb.ExecRequest{Command: "must-not-run"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("Exec error = %v, want Unauthenticated", err)
	}
	testServer.mu.Lock()
	secretCalls, sessionExecs := testServer.secretCalls, testServer.sessionExecs
	testServer.mu.Unlock()
	if secretCalls != 1 || sessionExecs != 0 {
		t.Fatalf("server calls: secret=%d session_exec=%d; want 1,0", secretCalls, sessionExecs)
	}
}

func TestNeedsTCPTunnel(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"https://abc-def.trycloudflare.com", true},     // legacy quick tunnel
		{"abc.trycloudflare.com:443", true},             // quick tunnel with port
		{"https://et-srv-hetzner-01-03.agend.sh", true}, // named pool tunnel
		{"et-srv-hetzner-01-03.agend.sh:443", true},     // named pool tunnel + port
		{"https://srv-hetzner-01-ctl.agend.sh", false},  // control plane (HTTP, not a TCP env tunnel)
		{"https://api.agend.sh", false},                 // control plane API
		{"localhost:50051", false},                      // local dev
	}
	for _, c := range cases {
		if got := needsTCPTunnel(c.addr); got != c.want {
			t.Errorf("needsTCPTunnel(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestIsPrivateAddr(t *testing.T) {
	private := []string{
		"localhost:50051",
		"localhost",
		"127.0.0.1:50051",
		"[::1]:50051",
		"10.1.2.3:50051",
		"172.16.0.2:50051", // dev VM TAP network
		"192.168.1.10:50051",
		"169.254.10.10:50051",
	}
	for _, addr := range private {
		if !isPrivateAddr(addr) {
			t.Errorf("isPrivateAddr(%q) = false, want true", addr)
		}
	}

	public := []string{
		"example.com:50051",
		"evil.example.com",
		"8.8.8.8:50051",
		"203.0.113.7:50051",
		"my-host.internal:50051", // DNS names are never trusted
	}
	for _, addr := range public {
		if isPrivateAddr(addr) {
			t.Errorf("isPrivateAddr(%q) = true, want false", addr)
		}
	}
}
