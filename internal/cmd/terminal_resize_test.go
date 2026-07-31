package cmd

import (
	"context"
	"sync"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type recordingPTYResizer struct {
	mu         sync.Mutex
	calls      [][2]uint32
	failures   int
	cancelDone context.CancelFunc
}

func (r *recordingPTYResizer) Resize(_ context.Context, columns, rows uint32) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, [2]uint32{columns, rows})
	if r.failures > 0 {
		r.failures--
		return status.Error(codes.FailedPrecondition, "PTY not active yet")
	}
	if r.cancelDone != nil {
		r.cancelDone()
	}
	return nil
}

func TestTerminalResizeForwarderWaitsForSessionAndForwardsSize(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	resizer := &recordingPTYResizer{failures: 1, cancelDone: cancel}
	events := make(chan struct{}, 1)
	events <- struct{}{}

	runTerminalResizeForwarder(ctx, resizer, events, func() (int, int, error) {
		return 132, 43, nil
	}, nil)

	resizer.mu.Lock()
	defer resizer.mu.Unlock()
	if len(resizer.calls) != 2 || resizer.calls[0] != [2]uint32{132, 43} || resizer.calls[1] != [2]uint32{132, 43} {
		t.Fatalf("resize calls = %#v", resizer.calls)
	}
}

func TestTerminalResizeForwarderDeduplicatesAndBoundsDimensions(t *testing.T) {
	resizer := &recordingPTYResizer{}
	events := make(chan struct{}, 2)
	events <- struct{}{}
	events <- struct{}{}
	close(events)

	runTerminalResizeForwarder(context.Background(), resizer, events, func() (int, int, error) {
		return 5000, 24, nil
	}, nil)

	resizer.mu.Lock()
	defer resizer.mu.Unlock()
	want := [][2]uint32{{1000, 24}}
	if len(resizer.calls) != len(want) || resizer.calls[0] != want[0] {
		t.Fatalf("resize calls = %#v, want %#v", resizer.calls, want)
	}
}
