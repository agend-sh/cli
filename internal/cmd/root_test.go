package cmd

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
)

type countingListener struct {
	net.Listener
	accepts atomic.Int32
}

func (l *countingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err == nil {
		l.accepts.Add(1)
	}
	return conn, err
}

func startCountingGRPCServer(t *testing.T) (*countingListener, string) {
	t.Helper()
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := &countingListener{Listener: base}
	server := grpc.NewServer()
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	return listener, listener.Addr().String()
}

func TestPingDoesNotReuseSchema2Endpoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	retiredListener, retiredAddr := startCountingGRPCServer(t)

	credentials := map[string]any{
		"version": 2,
		"active":  "old@acme.test",
		"accounts": map[string]any{
			"old@acme.test": map[string]any{
				"email":                 "old@acme.test",
				"token":                 "old-control-plane-token",
				"env_id":                "env-old",
				"endpoint":              retiredAddr,
				"secret":                "old-one-time-secret",
				"session_token":         "old-session-token",
				"control_plane_version": "v2",
			},
		},
	}
	data, err := json.Marshal(credentials)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, ".config", "agend")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.json"), data, 0600); err != nil {
		t.Fatal(err)
	}

	cmd := newPingCmd()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd.SetArgs([]string{"--env", "env-old"})
	if err := cmd.ExecuteContext(ctx); err == nil {
		t.Fatal("ping accepted retired account credentials")
	}
	if got := retiredListener.accepts.Load(); got != 0 {
		t.Fatalf("ping connected to schema2 endpoint %d time(s)", got)
	}
}
