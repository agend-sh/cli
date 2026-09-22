package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"
)

const connectProgressInterval = 200 * time.Millisecond

type connectProgress struct {
	writer io.Writer

	mu        sync.Mutex
	enabled   bool
	phase     string
	frame     int
	lineWidth int
	stopping  bool
	stop      chan struct{}
	done      chan struct{}
	stopOnce  sync.Once
}

func startConnectProgress(writer io.Writer) *connectProgress {
	progress := &connectProgress{writer: writer}
	file, ok := writer.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return progress
	}

	progress.enabled = true
	progress.phase = "Connecting to environment"
	progress.stop = make(chan struct{})
	progress.done = make(chan struct{})
	progress.mu.Lock()
	progress.renderLocked()
	progress.mu.Unlock()
	go progress.run()
	return progress
}

func (p *connectProgress) SetPhase(phase string) {
	if p == nil || !p.enabled {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopping {
		return
	}
	p.phase = phase
	p.renderLocked()
}

func (p *connectProgress) Stop() {
	if p == nil || !p.enabled {
		return
	}
	p.stopOnce.Do(func() {
		p.mu.Lock()
		p.stopping = true
		p.mu.Unlock()
		close(p.stop)
		<-p.done

		p.mu.Lock()
		_, _ = fmt.Fprintf(p.writer, "\r%s\r", strings.Repeat(" ", p.lineWidth))
		p.mu.Unlock()
	})
}

func (p *connectProgress) run() {
	ticker := time.NewTicker(connectProgressInterval)
	defer ticker.Stop()
	defer close(p.done)
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.mu.Lock()
			if !p.stopping {
				p.frame = (p.frame + 1) % 4
				p.renderLocked()
			}
			p.mu.Unlock()
		}
	}
}

func (p *connectProgress) renderLocked() {
	const frames = "|/-\\"
	line := fmt.Sprintf("%s %c", p.phase, frames[p.frame])
	padding := ""
	if missing := p.lineWidth - len(line); missing > 0 {
		padding = strings.Repeat(" ", missing)
	}
	_, _ = fmt.Fprintf(p.writer, "\r%s%s", line, padding)
	if len(line) > p.lineWidth {
		p.lineWidth = len(line)
	}
}
