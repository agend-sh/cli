package recovery

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestClassify_GRPCStatusCodes(t *testing.T) {
	cases := []struct {
		code codes.Code
		want Category
	}{
		{codes.Unauthenticated, Auth},
		{codes.NotFound, Fatal},
		{codes.PermissionDenied, Fatal},
		{codes.InvalidArgument, Fatal},
		{codes.Unavailable, Transient},
		{codes.DeadlineExceeded, Transient},
	}
	for _, c := range cases {
		err := status.Error(c.code, "x")
		if got := Classify(err); got != c.want {
			t.Errorf("Classify(%v) = %v, want %v", c.code, got, c.want)
		}
	}
}

func TestClassify_FallsBackToText(t *testing.T) {
	// A non-status error classifies on its message.
	if got := Classify(errors.New("dial tcp: no such host")); got != StaleEndpoint {
		t.Errorf("got %v, want StaleEndpoint", got)
	}
	if got := Classify(errors.New("totally unknown")); got != StaleEndpoint {
		t.Errorf("unknown should default to StaleEndpoint, got %v", got)
	}
}

func TestIsIdempotent(t *testing.T) {
	// Read-only ops are retryable.
	for _, tool := range []string{"port_list", "file_download", "shell_task_output", "env_status"} {
		if !IsIdempotent(tool) {
			t.Errorf("%s should be idempotent", tool)
		}
	}
	// Side-effecting ops must not be transparently retried.
	for _, tool := range []string{"shell_exec", "file_upload", "port_expose", "file_write", "env_create"} {
		if IsIdempotent(tool) {
			t.Errorf("%s must NOT be idempotent", tool)
		}
	}
}
