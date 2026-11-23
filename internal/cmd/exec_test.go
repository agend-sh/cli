package cmd

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/agend-sh/cli/internal/auth"
	pb "github.com/agend-sh/cli/proto/agentd/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestExecRequiresEnvironment(t *testing.T) {
	for _, args := range [][]string{{"pwd"}, {"--env=", "pwd"}, {"--env", "../other", "pwd"}} {
		cmd := newExecCmd()
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "env") {
			t.Fatalf("exec %q: expected environment argument error, got %v", args, err)
		}
	}
}

type execTargetServer struct {
	pb.UnimplementedAgentServiceServer
	execs atomic.Int32
}

func (s *execTargetServer) Ping(ctx context.Context, _ *pb.PingRequest) (*pb.PingResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if got := md.Get("x-one-time-secret"); len(got) != 1 || got[0] != "fresh" {
		return nil, status.Error(codes.Unauthenticated, "expired secret")
	}
	return &pb.PingResponse{}, grpc.SetTrailer(ctx, metadata.Pairs("x-session-token", "target-session"))
}

func (s *execTargetServer) Exec(ctx context.Context, r *pb.ExecRequest) (*pb.ExecResponse, error) {
	md, _ := metadata.FromIncomingContext(ctx)
	if got := md.Get("x-session-token"); len(got) != 1 || got[0] != "target-session" || r.Command != "echo hello" {
		return nil, status.Error(codes.InvalidArgument, "unexpected credentials or command")
	}
	s.execs.Add(1)
	return &pb.ExecResponse{}, nil
}

func TestExecKeepsExplicitTargetThroughRecovery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	agent := &execTargetServer{}
	pb.RegisterAgentServiceServer(server, agent)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v2/environments/env-target":
			fmt.Fprintf(w, `{"env_id":"env-target","state":"running","endpoint":%q,"secret":"expired"}`, listener.Addr().String())
		case "POST /v2/environments/env-target/reauth":
			// Another process updates its own credentials while recovery is in flight.
			if err := auth.SaveEnvironment("env-other", "localhost:1", "other-secret"); err != nil {
				t.Error(err)
			}
			fmt.Fprint(w, `{"env_id":"env-target","secret":"fresh"}`)
		default:
			t.Errorf("unexpected target: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer control.Close()
	if err := auth.SaveToken("test-token"); err != nil {
		t.Fatal(err)
	}
	if err := auth.SaveAPIURL(control.URL); err != nil {
		t.Fatal(err)
	}
	cmd := newExecCmd()
	cmd.SetArgs([]string{"--env", "env-target", "--", "echo", "hello"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	id, _, secret, _, err := auth.LoadEnvironment("env-other")
	if err != nil || id != "env-other" || secret != "other-secret" || agent.execs.Load() != 1 {
		t.Fatalf("selection %q, secret %q, executions %d, err %v", id, secret, agent.execs.Load(), err)
	}
}
