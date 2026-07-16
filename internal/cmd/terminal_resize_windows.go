//go:build windows

package cmd

import (
	"context"
	"sync"
	"time"
)

// Windows has no SIGWINCH. Polling feeds the same deduplicating forwarder and
// keeps resize behavior equivalent for long-running interactive calls.
func platformTerminalResizeEvents(ctx context.Context) (<-chan struct{}, func()) {
	events := make(chan struct{}, 1)
	events <- struct{}{}
	stop := make(chan struct{})
	var stopOnce sync.Once
	cleanup := func() { stopOnce.Do(func() { close(stop) }) }
	go func() {
		defer close(events)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-stop:
				return
			case <-ticker.C:
				select {
				case events <- struct{}{}:
				default:
				}
			}
		}
	}()
	return events, cleanup
}
