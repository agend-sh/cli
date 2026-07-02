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

func TestClassifyText_WindowsSocketErrors(t *testing.T) {
	// Windows spells socket errors differently from unix errno strings.
	cases := []struct {
		msg  string
		want Category
	}{
		{"read tcp 127.0.0.1:50051: wsarecv: An existing connection was forcibly closed by the remote host.", Transient},
		{"dial tcp 127.0.0.1:50051: connectex: No connection could be made because the target machine actively refused it.", StaleEndpoint},
		// Unix spellings keep classifying the same way.
		{"read tcp: connection reset by peer", Transient},
		{"dial tcp: connect: connection refused", StaleEndpoint},
	}
	for _, c := range cases {
		if got := ClassifyText(c.msg); got != c.want {
			t.Errorf("ClassifyText(%q) = %v, want %v", c.msg, got, c.want)
		}
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
