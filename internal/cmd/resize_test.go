package cmd

import "testing"

func TestParsePTYDimension(t *testing.T) {
	for _, test := range []struct {
		value string
		want  uint32
		ok    bool
	}{
		{value: "1", want: 1, ok: true},
		{value: "80", want: 80, ok: true},
		{value: "1000", want: 1000, ok: true},
		{value: "0"},
		{value: "1001"},
		{value: "-1"},
		{value: "80.5"},
		{value: "wide"},
	} {
		got, err := parsePTYDimension(test.value, "columns")
		if test.ok && (err != nil || got != test.want) {
			t.Errorf("parsePTYDimension(%q) = %d, %v; want %d", test.value, got, err, test.want)
		}
		if !test.ok && err == nil {
			t.Errorf("parsePTYDimension(%q) = %d, want error", test.value, got)
		}
	}
}
