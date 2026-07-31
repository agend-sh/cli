package mcp

import (
	"strings"
	"testing"
)

func TestValidateToolArgumentsRejectsLocalErrorsBeforeConnection(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGEND_LOCAL_ROOT", root)

	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "file_download",
			args: map[string]any{"local_path": root + "/../escape"},
			want: "outside the allowed directory",
		},
		{
			name: "shell_exec",
			args: map[string]any{
				"interactive":       true,
				"run_in_background": true,
			},
			want: "mutually exclusive",
		},
		{
			name: "shell_resize",
			args: map[string]any{"columns": float64(0), "rows": float64(24)},
			want: "columns must be an integer",
		},
		{
			name: "port_expose",
			args: map[string]any{"port": float64(65536)},
			want: "port must be an integer",
		},
		{
			name: "not_a_tool",
			args: map[string]any{},
			want: "unknown tool",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateToolArguments(test.name, test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateToolArguments() = %v, want error containing %q", err, test.want)
			}
		})
	}
}

func TestValidateToolArgumentsAcceptsValidLocalInputs(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGEND_LOCAL_ROOT", root)

	for _, test := range []struct {
		name string
		args map[string]any
	}{
		{"file_download", map[string]any{"local_path": root + "/new-file"}},
		{"file_upload", map[string]any{"local_path": root + "/not-created-yet"}},
		{"shell_exec", map[string]any{"interactive": true}},
		{"shell_resize", map[string]any{"columns": float64(80), "rows": float64(24)}},
		{"port_expose", map[string]any{"port": "8080"}},
		{"shell_task_output", map[string]any{"task_id": "task_1"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateToolArguments(test.name, test.args); err != nil {
				t.Fatalf("validateToolArguments() = %v", err)
			}
		})
	}
}
