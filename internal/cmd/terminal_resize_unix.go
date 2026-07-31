//go:build !windows

package cmd

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

func platformTerminalResizeEvents(ctx context.Context) (<-chan struct{}, func()) {
	events := make(chan struct{}, 1)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGWINCH)
	events <- struct{}{}

	var stopOnce sync.Once
	stop := func() {
		stopOnce.Do(func() { signal.Stop(signals) })
	}
	go func() {
		defer close(events)
		defer stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-signals:
				select {
				case events <- struct{}{}:
				default:
				}
			}
		}
	}()
	return events, stop
}
