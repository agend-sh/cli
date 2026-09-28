package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentgrpc "github.com/agend-sh/cli/internal/grpc"
	pb "github.com/agend-sh/cli/proto/agentd/v1"
)

const (
	// watchTypingMaxRunes bounds the printable length of an update that
	// --typing-delay paces. Longer updates are program output, not an echo
	// of typed input, and appear at once.
	watchTypingMaxRunes = 80

	// watchClearScreen starts a mirrored session from a blank screen, as the
	// session's own PTY started.
	watchClearScreen = "\x1b[0m\x1b[H\x1b[2J"
	// watchResetTerminal undoes modes a mirrored program may have left on:
	// alternate screen, hidden cursor, mouse and focus reporting, bracketed
	// paste, application cursor and keypad keys, and modifyOtherKeys (which
	// vim enables). Stopping mid-session must not leave the local shell
	// receiving encoded keys.
	watchResetTerminal = "\x1b[0m\x1b[?1049l\x1b[?25h\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l" +
		"\x1b[?1004l\x1b[?2004l\x1b[?1l\x1b>\x1b[>4m"
)

// errWatchUnsupported is fatal: retrying the same host worker cannot help.
var errWatchUnsupported error = watchFatalError(
	"this environment's host worker does not support watch yet; it needs a newer agend release")

// watchFatalError prints as its message and classifies as a failed
// precondition, so connection recovery does not retry it.
type watchFatalError string

func (e watchFatalError) Error() string { return string(e) }

func (e watchFatalError) GRPCStatus() *status.Status {
	return status.New(codes.FailedPrecondition, string(e))
}

func newWatchCmd() *cobra.Command {
	var addr string
	var typingDelay time.Duration

	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Mirror the environment's interactive session, read-only",
		Long: `Mirror the interactive session running in the environment, read-only.

watch shows what an agent does in its interactive session (shell_exec with
interactive=true, then shell_send_raw or shell_provide_input), or what a person
types through 'agend connect'. It replays what the session has printed so far,
then follows it live. When the session ends, watch waits for the next one.

watch never sends input to the session. Press q or Ctrl-C to stop watching.
For an exact mirror, size this terminal to match the session; watch warns
when it is smaller.

--typing-delay paces short updates, such as the echo of typed keystrokes, one
character at a time, which reads better in a screen recording.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if typingDelay < 0 {
				return fmt.Errorf("--typing-delay must not be negative")
			}
			return runWatch(cmd, addr, typingDelay)
		},
	}

	cmd.Flags().StringVar(&addr, "addr", "localhost:50051", "agentd address")
	cmd.Flags().DurationVar(&typingDelay, "typing-delay", 0, "delay between characters of short updates, e.g. 40ms")
	return cmd
}

func runWatch(cmd *cobra.Command, addr string, typingDelay time.Duration) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	watchCtx, quit := context.WithCancel(ctx)
	defer quit()

	stdoutTerminal := term.IsTerminal(int(os.Stdout.Fd()))
	renderer := &watchRenderer{
		ctx: watchCtx, out: os.Stdout, status: cmd.ErrOrStderr(),
		terminal: stdoutTerminal, typingDelay: typingDelay, localSize: stdoutTerminalSize,
	}

	// Raw mode keeps keystrokes from echoing over the mirror. Ctrl-C then
	// arrives as a byte, so the key reader handles it.
	if fd := int(os.Stdin.Fd()); term.IsTerminal(fd) {
		oldState, err := term.MakeRaw(fd)
		if err != nil {
			return fmt.Errorf("enable raw terminal mode: %w", err)
		}
		defer func() { _ = term.Restore(fd, oldState) }()
		go readWatchQuitKeys(os.Stdin, quit)
	}
	if stdoutTerminal {
		defer func() { _, _ = io.WriteString(os.Stdout, watchResetTerminal) }()
	}

	renderer.statusLine("waiting for an interactive session (q to quit)")
	err := callWithRetry(watchCtx, cmd, addr, true, func(client *agentgrpc.Client) error {
		return followWatchStream(watchCtx, client, renderer)
	})
	if watchCtx.Err() != nil {
		// q, Ctrl-C, or a signal: stopping is the expected way out.
		return nil
	}
	return err
}

func followWatchStream(ctx context.Context, client *agentgrpc.Client, renderer *watchRenderer) error {
	stream, err := client.Agent.Watch(ctx, &pb.WatchRequest{})
	if err != nil {
		return watchStreamError(err)
	}
	for {
		event, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return watchStreamError(err)
		}
		if err := renderer.handle(event); err != nil {
			return err
		}
	}
}

func watchStreamError(err error) error {
	switch status.Code(err) {
	case codes.Unimplemented:
		return errWatchUnsupported
	case codes.ResourceExhausted:
		return watchFatalError(status.Convert(err).Message())
	}
	return err
}

// readWatchQuitKeys calls quit on q, Ctrl-C, or Ctrl-D, and ignores other
// keys: watch is read-only. The mirrored program can make the local terminal
// answer queries on stdin; an answer starts with ESC, and nothing after an
// ESC in one read counts as a key.
func readWatchQuitKeys(stdin io.Reader, quit func()) {
	buffer := make([]byte, 64)
	for {
		read, err := stdin.Read(buffer)
	keys:
		for _, key := range buffer[:read] {
			switch key {
			case 0x1b:
				break keys
			case 'q', 'Q', 0x03, 0x04:
				quit()
				return
			}
		}
		if err != nil {
			return
		}
	}
}

type watchRenderer struct {
	ctx         context.Context
	out         io.Writer
	status      io.Writer
	terminal    bool
	typingDelay time.Duration
	localSize   terminalSizeReader

	sanitizer     connectStreamSanitizer
	replaying     uint64
	columns, rows uint32
}

func (r *watchRenderer) handle(event *pb.WatchResponse) error {
	switch payload := event.GetEvent().(type) {
	case *pb.WatchResponse_Session:
		session := payload.Session
		r.sanitizer = connectStreamSanitizer{}
		r.replaying = session.GetHistoryBytes()
		r.columns, r.rows = session.GetColumns(), session.GetRows()
		if r.terminal {
			if err := r.write(watchResetTerminal + watchClearScreen); err != nil {
				return err
			}
		}
		if session.GetHistoryTruncated() {
			r.statusLine("the start of this session is no longer retained; the replay begins mid-session")
		}
		r.warnIfSmaller()
	case *pb.WatchResponse_Output:
		return r.output(payload.Output)
	case *pb.WatchResponse_Resize:
		r.columns, r.rows = payload.Resize.GetColumns(), payload.Resize.GetRows()
		r.warnIfSmaller()
	case *pb.WatchResponse_Skipped:
		r.statusLine(fmt.Sprintf("fell behind; %d bytes of output were skipped", payload.Skipped))
	case *pb.WatchResponse_Ended:
		if r.terminal {
			if err := r.write(watchResetTerminal); err != nil {
				return err
			}
		}
		r.statusLine("session ended; waiting for the next one (q to quit)")
	}
	return nil
}

func (r *watchRenderer) output(data []byte) error {
	text := string(data)
	if r.terminal {
		text = r.sanitizer.sanitize(text)
	}
	if r.replaying > 0 {
		// Replayed history appears at once; only live output is paced.
		r.replaying -= min(r.replaying, uint64(len(data)))
		return r.write(text)
	}
	return r.writePaced(text)
}

// writePaced writes the first line of a short update one character at a
// time, keeping escape sequences whole, and the rest at once. The first line
// of a live update is usually the echo of what was typed.
func (r *watchRenderer) writePaced(text string) error {
	if r.typingDelay <= 0 || text == "" {
		return r.write(text)
	}
	lineEnd := strings.IndexAny(text, "\r\n")
	if lineEnd < 0 {
		lineEnd = len(text)
	}
	line := text[:lineEnd]
	if printableRunes(line) > watchTypingMaxRunes {
		return r.write(text)
	}
	for i := 0; i < len(line); {
		if line[i] == 0x1b {
			end, _ := scanEscape(line, i)
			if err := r.write(line[i:end]); err != nil {
				return err
			}
			i = end
			continue
		}
		_, size := utf8.DecodeRuneInString(line[i:])
		if err := r.write(line[i : i+size]); err != nil {
			return err
		}
		i += size
		if line[i-size] >= 0x20 {
			select {
			case <-r.ctx.Done():
				return r.ctx.Err()
			case <-time.After(r.typingDelay):
			}
		}
	}
	return r.write(text[lineEnd:])
}

func printableRunes(s string) int {
	count := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i, _ = scanEscape(s, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r >= 0x20 {
			count++
		}
		i += size
	}
	return count
}

func (r *watchRenderer) write(text string) error {
	if text == "" {
		return nil
	}
	if _, err := io.WriteString(r.out, text); err != nil {
		return fmt.Errorf("watch: write terminal: %w", err)
	}
	return nil
}

// statusLine reports watch's own state, dimmed, on its own line. It goes to
// stderr so a redirected mirror stays byte-exact.
func (r *watchRenderer) statusLine(message string) {
	_, _ = fmt.Fprintf(r.status, "\r\n\x1b[2m[agend watch] %s\x1b[0m\r\n", message)
}

func (r *watchRenderer) warnIfSmaller() {
	if !r.terminal || r.columns == 0 || r.rows == 0 {
		return
	}
	columns, rows, ok := normalizedTerminalSize(r.localSize)
	if !ok || (columns >= r.columns && rows >= r.rows) {
		return
	}
	r.statusLine(fmt.Sprintf("the session is %dx%d but this terminal is %dx%d; enlarge it for an exact mirror",
		r.columns, r.rows, columns, rows))
}
