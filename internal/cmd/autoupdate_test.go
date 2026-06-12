package cmd

import "testing"

func TestIsNewer(t *testing.T) {
	cases := []struct {
		tag, cur string
		want     bool
	}{
		{"v1.1.4", "v1.1.3", true},
		{"v1.2.0", "v1.1.9", true},
		{"v2.0.0", "v1.9.9", true},
		{"v1.1.3", "v1.1.3", false},
		{"v1.1.2", "v1.1.3", false}, // never downgrade
		{"v1.1.3", "1.1.3", false},  // equal, mixed prefix
		{"v1.1.4", "1.1.3", true},   // mixed prefix, newer
		{"v1.1.4-rc1", "v1.1.3", true},
		{"garbage", "v1.1.3", false},
		{"v1.1.4", "0.0.0-dev", true}, // dev parses to 0.0.0
		{"", "v1.1.3", false},
	}
	for _, c := range cases {
		if got := isNewer(c.tag, c.cur); got != c.want {
			t.Errorf("isNewer(%q, %q) = %v, want %v", c.tag, c.cur, got, c.want)
		}
	}
}

func TestFirstSubcommand(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"agend", "env", "list"}, "env"},
		// Subcommand-local flags (e.g. --addr) come AFTER the subcommand; the
		// root has no value-taking persistent flags, so the first non-flag token
		// is always the subcommand.
		{[]string{"agend", "exec", "--addr", "x", "ls"}, "exec"},
		{[]string{"agend", "mcp"}, "mcp"},
		{[]string{"agend"}, ""},
		{[]string{"agend", "--version"}, ""},
	}
	for _, c := range cases {
		if got := firstSubcommand(c.args); got != c.want {
			t.Errorf("firstSubcommand(%v) = %q, want %q", c.args, got, c.want)
		}
	}
}

func TestAutoUpdateEnabled(t *testing.T) {
	t.Setenv("AGEND_NO_AUTOUPDATE", "")
	t.Setenv("AGEND_SELF_UPDATED", "")

	if !autoUpdateEnabled("v1.1.3", []string{"agend", "exec", "ls"}) {
		t.Error("expected enabled for a release build running a normal command")
	}
	// Gated off cases.
	off := []struct {
		name    string
		version string
		args    []string
		env     map[string]string
	}{
		{"dev build", "0.0.0-dev", []string{"agend", "exec"}, nil},
		{"empty version", "", []string{"agend", "exec"}, nil},
		{"update command", "v1.1.3", []string{"agend", "update"}, nil},
		{"version command", "v1.1.3", []string{"agend", "version"}, nil},
		{"--version flag", "v1.1.3", []string{"agend", "--version"}, nil},
		{"-h flag", "v1.1.3", []string{"agend", "exec", "-h"}, nil},
		{"opt-out env", "v1.1.3", []string{"agend", "exec"}, map[string]string{"AGEND_NO_AUTOUPDATE": "1"}},
		{"already re-exec'd", "v1.1.3", []string{"agend", "exec"}, map[string]string{"AGEND_SELF_UPDATED": "1"}},
	}
	for _, c := range off {
		t.Run(c.name, func(t *testing.T) {
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			if autoUpdateEnabled(c.version, c.args) {
				t.Errorf("expected auto-update disabled for %s", c.name)
			}
		})
	}
}
