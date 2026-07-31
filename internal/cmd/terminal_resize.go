package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentgrpc "github.com/agend-sh/cli/internal/grpc"
)

const (
	initialResizeRetryDelay  = 50 * time.Millisecond
	initialResizeRetryWindow = 10 * time.Second
)

type ptyResizeClient interface {
	Resize(context.Context, uint32, uint32) error
}

type terminalSizeReader func() (width, height int, err error)

func stdoutTerminalSize() (int, int, error) {
	return term.GetSize(int(os.Stdout.Fd()))
}

func normalizedTerminalSize(readSize terminalSizeReader) (uint32, uint32, bool) {
	width, height, err := readSize()
	if err != nil || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	if width > int(agentgrpc.MaxPTYDimension) {
		width = int(agentgrpc.MaxPTYDimension)
	}
	if height > int(agentgrpc.MaxPTYDimension) {
		height = int(agentgrpc.MaxPTYDimension)
	}
	return uint32(width), uint32(height), true
}

func terminalResizeAvailable() bool {
	_, _, ok := normalizedTerminalSize(stdoutTerminalSize)
	return ok
}

func resizePTYWhenReady(ctx context.Context, client ptyResizeClient, columns, rows uint32, waitForSession bool) error {
	if !waitForSession {
		callCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		return client.Resize(callCtx, columns, rows)
	}

	retryCtx, cancel := context.WithTimeout(ctx, initialResizeRetryWindow)
	defer cancel()
	for {
		callCtx, callCancel := context.WithTimeout(retryCtx, 4*time.Second)
		err := client.Resize(callCtx, columns, rows)
		callCancel()
		if err == nil {
			return nil
		}
		if status.Code(err) != codes.FailedPrecondition {
			return err
		}
		timer := time.NewTimer(initialResizeRetryDelay)
		select {
		case <-retryCtx.Done():
			timer.Stop()
			return retryCtx.Err()
		case <-timer.C:
		}
	}
}

func runTerminalResizeForwarder(
	ctx context.Context,
	client ptyResizeClient,
	events <-chan struct{},
	readSize terminalSizeReader,
	report func(error),
) {
	var lastColumns, lastRows uint32
	initial := true
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-events:
			if !ok {
				return
			}
			columns, rows, valid := normalizedTerminalSize(readSize)
			if !valid || columns == lastColumns && rows == lastRows {
				continue
			}
			err := resizePTYWhenReady(ctx, client, columns, rows, initial)
			initial = false
			if err != nil {
				// A resize can race with normal session completion. There is no
				// active PTY in that case, so FailedPrecondition needs no warning.
				if status.Code(err) != codes.FailedPrecondition && ctx.Err() == nil && report != nil {
					report(err)
				}
				continue
			}
			lastColumns, lastRows = columns, rows
		}
	}
}

// startTerminalResizeForwarding sends the current terminal size once the
// remote PTY exists and then forwards platform resize events for the duration
// of the interactive Exec RPC.
func startTerminalResizeForwarding(parent context.Context, client ptyResizeClient, warnings io.Writer) func() {
	ctx, cancel := context.WithCancel(parent)
	events, stopEvents := platformTerminalResizeEvents(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runTerminalResizeForwarder(ctx, client, events, stdoutTerminalSize, func(err error) {
			_, _ = fmt.Fprintf(warnings, "warning: terminal resize was not forwarded: %v\n", err)
		})
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			stopEvents()
			<-done
		})
	}
}
