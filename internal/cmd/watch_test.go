package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	ggrpc "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentgrpc "github.com/agend-sh/cli/internal/grpc"
	"github.com/agend-sh/cli/internal/recovery"
	pb "github.com/agend-sh/cli/proto/agentd/v1"
)

// recordingWriter keeps each write separately, so tests can see pacing.
type recordingWriter struct{ writes []string }

func (w *recordingWriter) Write(p []byte) (int, error) {
	w.writes = append(w.writes, string(p))
	return len(p), nil
}

func (w *recordingWriter) String() string { return strings.Join(w.writes, "") }

func newTestWatchRenderer(terminal bool, delay time.Duration) (*watchRenderer, *recordingWriter, *bytes.Buffer) {
	out := &recordingWriter{}
	statusOut := &bytes.Buffer{}
	return &watchRenderer{
		ctx: context.Background(), out: out, status: statusOut, terminal: terminal,
		typingDelay: delay, localSize: func() (int, int, error) { return 200, 60, nil },
	}, out, statusOut
}

func watchSessionEvent(command string, columns, rows uint32, historyBytes int) *pb.WatchResponse {
	return &pb.WatchResponse{Event: &pb.WatchResponse_Session{Session: &pb.WatchSession{
		Command: command, Columns: columns, Rows: rows, HistoryBytes: uint64(historyBytes),
	}}}
}

func watchOutputEvent(output string) *pb.WatchResponse {
	return &pb.WatchResponse{Event: &pb.WatchResponse_Output{Output: []byte(output)}}
}

func TestWatchRendererClearsReplaysAndPacesLiveEcho(t *testing.T) {
	renderer, out, _ := newTestWatchRenderer(true, time.Microsecond)
	history := "$ ls\r\nREADME.md\r\n$ "
	for _, event := range []*pb.WatchResponse{
		watchSessionEvent("bash", 120, 24, len(history)),
		watchOutputEvent(history),
	} {
		if err := renderer.handle(event); err != nil {
			t.Fatal(err)
		}
	}
	if len(out.writes) != 2 || out.writes[0] != watchResetTerminal+watchClearScreen || out.writes[1] != history {
		t.Fatalf("session start writes = %q, want a reset, a clear, then the history at once", out.writes)
	}

	out.writes = nil
	if err := renderer.handle(watchOutputEvent("vim\x1b[1mé\x1b[0m\r\nopening...\r\n")); err != nil {
		t.Fatal(err)
	}
	want := []string{"v", "i", "m", "\x1b[1m", "é", "\x1b[0m", "\r\nopening...\r\n"}
	if strings.Join(out.writes, "|") != strings.Join(want, "|") {
		t.Fatalf("live writes = %q, want %q", out.writes, want)
	}
}

func TestWatchRendererWritesLongUpdatesAtOnce(t *testing.T) {
	renderer, out, _ := newTestWatchRenderer(true, time.Hour)
	if err := renderer.handle(watchSessionEvent("vim", 120, 24, 0)); err != nil {
		t.Fatal(err)
	}
	out.writes = nil
	redraw := strings.Repeat("~", watchTypingMaxRunes+1)
	if err := renderer.handle(watchOutputEvent(redraw)); err != nil {
		t.Fatal(err)
	}
	if len(out.writes) != 1 || out.writes[0] != redraw {
		t.Fatalf("writes = %q, want the redraw in one write", out.writes)
	}
}

func TestWatchRendererSanitizesOnlyATerminal(t *testing.T) {
	const output = "\x1b]52;c;cGF3bmVk\x07\x1b[31mred\x1b[0m"
	for _, terminal := range []bool{true, false} {
		renderer, out, _ := newTestWatchRenderer(terminal, 0)
		for _, event := range []*pb.WatchResponse{watchSessionEvent("bash", 0, 0, 0), watchOutputEvent(output)} {
			if err := renderer.handle(event); err != nil {
				t.Fatal(err)
			}
		}
		got := out.String()
		if terminal && (strings.Contains(got, "\x1b]52") || !strings.Contains(got, "\x1b[31mred")) {
			t.Fatalf("terminal output = %q, want OSC stripped and SGR kept", got)
		}
		if !terminal && got != output {
			t.Fatalf("redirected output = %q, want it byte-exact", got)
		}
	}
}

func TestWatchRendererReportsSizeAndSessionChanges(t *testing.T) {
	renderer, out, statusOut := newTestWatchRenderer(true, 0)
	if err := renderer.handle(watchSessionEvent("vim", 120, 24, 0)); err != nil {
		t.Fatal(err)
	}
	if statusOut.Len() != 0 {
		t.Fatalf("status = %q, want none while the terminal is large enough", statusOut)
	}
	if err := renderer.handle(&pb.WatchResponse{Event: &pb.WatchResponse_Resize{
		Resize: &pb.WatchResize{Columns: 300, Rows: 40},
	}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statusOut.String(), "the session is 300x40 but this terminal is 200x60") {
		t.Fatalf("status = %q, want a size warning", statusOut)
	}

	statusOut.Reset()
	out.writes = nil
	if err := renderer.handle(&pb.WatchResponse{Event: &pb.WatchResponse_Ended{Ended: true}}); err != nil {
		t.Fatal(err)
	}
	if out.String() != watchResetTerminal || !strings.Contains(statusOut.String(), "session ended") {
		t.Fatalf("end writes = %q status = %q", out.writes, statusOut)
	}
}

func TestReadWatchQuitKeysIgnoresOtherKeys(t *testing.T) {
	quit := make(chan struct{})
	go readWatchQuitKeys(strings.NewReader("ix:wq\n"), func() { close(quit) })
	select {
	case <-quit:
	case <-time.After(2 * time.Second):
		t.Fatal("q did not quit")
	}

	quits := 0
	readWatchQuitKeys(strings.NewReader("ix:w\n"), func() { quits++ })
	if quits != 0 {
		t.Fatalf("quit %d times without a quit key", quits)
	}
}

func TestWatchStreamErrorForAnOlderHostWorkerIsFatal(t *testing.T) {
	err := watchStreamError(status.Error(codes.Unimplemented, "unknown method Watch"))
	if !errors.Is(err, errWatchUnsupported) || recovery.Classify(err) != recovery.Fatal ||
		strings.Contains(err.Error(), "rpc error") {
		t.Fatalf("err = %v (%v), want a plain fatal unsupported error", err, recovery.Classify(err))
	}
	busy := watchStreamError(status.Error(codes.ResourceExhausted, "at most 8 watchers per environment"))
	if recovery.Classify(busy) != recovery.Fatal || busy.Error() != "at most 8 watchers per environment" {
		t.Fatalf("busy = %v (%v), want a plain fatal error", busy, recovery.Classify(busy))
	}
}

type watchTestServer struct {
	pb.UnimplementedAgentServiceServer
	events []*pb.WatchResponse
}

func (s *watchTestServer) Watch(_ *pb.WatchRequest, stream pb.AgentService_WatchServer) error {
	for _, event := range s.events {
		if err := stream.Send(event); err != nil {
			return err
		}
	}
	return nil
}

func TestFollowWatchStreamRendersTheServerStream(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := ggrpc.NewServer()
	pb.RegisterAgentServiceServer(grpcServer, &watchTestServer{events: []*pb.WatchResponse{
		watchSessionEvent("vim", 120, 24, 5),
		watchOutputEvent("hello"),
		{Event: &pb.WatchResponse_Keepalive{Keepalive: true}},
		watchOutputEvent(" world"),
		{Event: &pb.WatchResponse_Ended{Ended: true}},
	}})
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := agentgrpc.Dial(ctx, listener.Addr().String(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	renderer, out, statusOut := newTestWatchRenderer(false, 0)
	if err := followWatchStream(ctx, client, renderer); err != nil {
		t.Fatalf("followWatchStream: %v", err)
	}
	if out.String() != "hello world" || !strings.Contains(statusOut.String(), "session ended") {
		t.Fatalf("output = %q status = %q", out.String(), statusOut)
	}
}

func TestFollowWatchStreamReportsAnOlderHostWorker(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	grpcServer := ggrpc.NewServer()
	pb.RegisterAgentServiceServer(grpcServer, &pb.UnimplementedAgentServiceServer{})
	go func() { _ = grpcServer.Serve(listener) }()
	t.Cleanup(grpcServer.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := agentgrpc.Dial(ctx, listener.Addr().String(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	renderer, _, _ := newTestWatchRenderer(false, 0)
	renderer.status = io.Discard
	if err := followWatchStream(ctx, client, renderer); !errors.Is(err, errWatchUnsupported) {
		t.Fatalf("followWatchStream error = %v, want errWatchUnsupported", err)
	}
}
