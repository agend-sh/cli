package mcp

import (
	"encoding/json"
	"testing"
)

func TestNumArg(t *testing.T) {
	cases := []struct {
		in   any
		want float64
	}{
		{float64(8080), 8080},
		{"8080", 8080},          // LLMs pass numbers as strings — must parse, not 0
		{"  443 ", 443},         // tolerate whitespace
		{json.Number("22"), 22}, // json.Number path
		{"notanumber", 0},       // unparseable → 0 (caller validates)
		{nil, 0},
		{true, 0},
	}
	for _, c := range cases {
		if got := numArg(c.in); got != c.want {
			t.Errorf("numArg(%#v) = %v, want %v", c.in, got, c.want)
		}
	}
}
