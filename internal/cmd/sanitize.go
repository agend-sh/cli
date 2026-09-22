package cmd

import (
	"os"
	"strings"

	"golang.org/x/term"
)

// sanitizeRemote makes remote-environment output safe to print on the local
// TTY. The environment is untrusted: raw escape sequences could move the
// cursor, rewrite scrollback, set the window title, or abuse OSC 52 to write
// the clipboard. SGR sequences (colors/bold — ESC [ ... m) are preserved;
// every other escape sequence and control character (except \n, \r, \t) is
// stripped.
// sanitizeForTTY sanitizes s only when f is a terminal. Escape injection is
// only dangerous on a TTY; piped/redirected output must stay byte-exact so
// things like `agend exec cat file > out` aren't corrupted.
func sanitizeForTTY(s string, f *os.File) string {
	if term.IsTerminal(int(f.Fd())) {
		return sanitizeRemote(s)
	}
	return s
}

func sanitizeRemote(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(s); {
		c := s[i]

		if c == 0x1b { // ESC — parse and either keep (SGR) or skip the sequence
			seqEnd, isSGR := scanEscape(s, i)
			if isSGR {
				b.WriteString(s[i:seqEnd])
			}
			i = seqEnd
			continue
		}

		if c == '\n' || c == '\r' || c == '\t' || (c >= 0x20 && c != 0x7f) {
			b.WriteByte(c)
		}
		i++
	}

	return b.String()
}

// sanitizeConnectStream sanitizes raw PTY output for a live interactive shell
// (agend connect). Unlike sanitizeRemote (used for one-shot exec/input text
// output, where control sequences serve no purpose and are dropped except
// SGR colors), this is a real terminal client: it needs full CSI fidelity —
// cursor movement, clear screen, alt-screen switching — for TUI apps (vim,
// htop) to render correctly, the same as any SSH client. Only OSC/DCS/PM/APC
// sequences are stripped: they carry no rendering purpose those apps need,
// and OSC includes OSC 52, which can write the local clipboard.
func sanitizeConnectStream(s string) string {
	var sanitizer connectStreamSanitizer
	return sanitizer.sanitize(s)
}

// connectStreamSanitizer keeps an incomplete escape sequence between gRPC
// frames. PTY reads and HTTP/2 messages are independent boundaries; an ANSI
// CSI sequence split as "ESC[" + "31m" must still reach the local terminal
// as one sequence.
type connectStreamSanitizer struct {
	pending string
}

func (s *connectStreamSanitizer) sanitize(chunk string) string {
	input := s.pending + chunk
	s.pending = ""
	var b strings.Builder
	b.Grow(len(input))

	for i := 0; i < len(input); {
		c := input[i]

		if c == 0x1b { // ESC — parse and keep only a complete, non-OSC sequence
			seqEnd, keep := scanConnectEscape(input, i)
			if connectEscapeIncomplete(input, i, seqEnd) {
				s.pending = input[i:]
				break
			}
			if keep {
				b.WriteString(input[i:seqEnd])
			}
			i = seqEnd
			continue
		}

		b.WriteByte(c)
		i++
	}

	return b.String()
}

func connectEscapeIncomplete(s string, start, end int) bool {
	if start+1 >= len(s) {
		return true
	}
	switch s[start+1] {
	case ']', 'P', 'X', '^', '_':
		return !strings.Contains(s[start+2:end], "\a") &&
			!strings.Contains(s[start+2:end], "\x1b\\")
	case '[':
		return end == len(s) && (end == start+2 || s[end-1] < 0x40 || s[end-1] > 0x7e)
	default:
		return false
	}
}

// scanConnectEscape parses the escape sequence starting at s[start] (ESC) and
// returns the index just past it, plus whether it should be kept (written
// verbatim). OSC/DCS/SOS/PM/APC sequences are never kept. A sequence
// truncated at EOF (no terminating byte, including a lone trailing ESC) is
// also dropped rather than written half-complete — the same conservative
// handling as scanEscape.
func scanConnectEscape(s string, start int) (end int, keep bool) {
	i := start + 1
	if i >= len(s) {
		return i, false // lone ESC at EOF
	}

	switch s[i] {
	case ']', 'P', 'X', '^', '_': // OSC / DCS / SOS / PM / APC: until BEL or ST
		i++
		for i < len(s) {
			if s[i] == 0x07 {
				return i + 1, false
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2, false
			}
			i++
		}
		return i, false

	case '[': // CSI: parameter bytes 0x30-0x3f, intermediate 0x20-0x2f, final 0x40-0x7e
		i++
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x3f {
			i++
		}
		if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e {
			return i + 1, true
		}
		return i, false // truncated, no final byte

	default: // two-byte sequence (ESC c, ESC 7, ...) — always complete here
		return i + 1, true
	}
}

// scanEscape parses the escape sequence starting at s[start] (which is ESC)
// and returns the index just past it, plus whether it is a safe SGR sequence.
func scanEscape(s string, start int) (end int, isSGR bool) {
	i := start + 1
	if i >= len(s) {
		return i, false
	}

	switch s[i] {
	case '[': // CSI: parameter bytes 0x30–0x3f, intermediate 0x20–0x2f, final 0x40–0x7e
		i++
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x3f {
			i++
		}
		if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e {
			return i + 1, s[i] == 'm'
		}
		return i, false

	case ']', 'P', 'X', '^', '_': // OSC / DCS / SOS / PM / APC: until BEL or ST (ESC \)
		i++
		for i < len(s) {
			if s[i] == 0x07 {
				return i + 1, false
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2, false
			}
			i++
		}
		return i, false

	default: // two-byte sequence (ESC c, ESC 7, ...)
		return i + 1, false
	}
}
