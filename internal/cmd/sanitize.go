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
