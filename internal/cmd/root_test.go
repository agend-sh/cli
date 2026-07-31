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

	"github.com/spf13/cobra"
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

// The command boundary must ignore environment authority from the previous
// credential schema. This exercises dialDaemon rather than only asserting the
// auth package's decoded values: the safe caller address is connected, while
// the stored retired-generation endpoint is never touched.
func TestDialDaemonDoesNotReuseSchema2Endpoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	retiredListener, retiredAddr := startCountingGRPCServer(t)
	safeListener, safeAddr := startCountingGRPCServer(t)

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

	cmd := &cobra.Command{}
	cmd.Flags().String("addr", safeAddr, "")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, err := dialDaemon(ctx, cmd, safeAddr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	if got := safeListener.accepts.Load(); got == 0 {
		t.Fatal("dialDaemon did not connect to the caller-provided safe address")
	}
	if got := retiredListener.accepts.Load(); got != 0 {
		t.Fatalf("dialDaemon connected to schema2 endpoint %d time(s)", got)
	}
}
