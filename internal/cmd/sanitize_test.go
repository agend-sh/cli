package cmd

import "testing"

func TestSanitizeRemote(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", "hello world\n", "hello world\n"},
		{"keeps SGR colors", "\x1b[31mred\x1b[0m", "\x1b[31mred\x1b[0m"},
		{"strips cursor movement", "a\x1b[2Ab", "ab"},
		{"strips clear screen", "\x1b[2Jhidden", "hidden"},
		{"strips OSC title (BEL)", "\x1b]0;evil title\x07text", "text"},
		{"strips OSC 52 clipboard (ST)", "\x1b]52;c;ZXZpbA==\x1b\\text", "text"},
		{"strips DCS", "\x1bPq#evil\x1b\\ok", "ok"},
		{"strips bare ESC pairs", "\x1bc reset", " reset"},
		{"strips C0 controls", "a\x08\x08b\x07", "ab"},
		{"keeps tabs and CRLF", "a\tb\r\n", "a\tb\r\n"},
		{"strips DEL", "a\x7fb", "ab"},
		{"truncated CSI at EOF", "x\x1b[31", "x"},
		{"truncated OSC at EOF", "x\x1b]0;title", "x"},
		{"lone ESC at EOF", "x\x1b", "x"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizeRemote(c.in); got != c.want {
				t.Errorf("sanitizeRemote(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
