package mcp

import "testing"

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want ErrorCategory
	}{
		// Fatal — client-side cancellation
		{"context canceled", "rpc error: code = Canceled desc = context canceled", ErrFatal},

		// Fatal — gRPC 4xx
		{"not found", "task_output failed: rpc error: code = NotFound desc = task not found", ErrFatal},
		{"permission denied", "file_download failed: rpc error: code = PermissionDenied desc = access denied", ErrFatal},
		{"invalid argument", "rpc error: code = InvalidArgument desc = invalid mode", ErrFatal},
		{"failed precondition", "rpc error: code = FailedPrecondition desc = no active session", ErrFatal},

		// Fatal — local MCP client policy/filesystem errors. These never came
		// from the endpoint and therefore must not trigger tunnel recovery.
		{"download outside local root",
			"download failed: local_path /tmp/escape is outside the allowed directory /tmp/root (set AGEND_LOCAL_ROOT to change it)",
			ErrFatal},
		{"upload path required", "upload failed: local_path is required", ErrFatal},
		{"local target symlink",
			"create local file: local_path /tmp/root/out is a symlink — refusing to write through it",
			ErrFatal},
		{"local upload read", "read local file: open /tmp/root/in: permission denied", ErrFatal},
		{"local download write", "write: no space left on device", ErrFatal},
		{"download checksum mismatch",
			"download failed: checksum mismatch (expected abc, got def) — file removed",
			ErrTransient},

		// Fatal — port_expose misconfig. These used to fall through to
		// ErrStaleEndpoint and trigger a 30s reconnect loop before surfacing
		// "environment unreachable". Now they return immediately with the
		// daemon's actionable message.
		{"hostname not in zone",
			"rpc error: code = Unknown desc = createDNS: app.example.com.br does not belong to Cloudflare zone agend.sh",
			ErrFatal},
		{"DNS bound elsewhere",
			"rpc error: code = Unknown desc = app.example.com.br already bound: CNAME points to foo.cfargotunnel.com",
			ErrFatal},

		// Auth
		{"unauthenticated", "rpc error: code = Unauthenticated desc = unauthenticated", ErrAuth},
		{"401 HTTP", "status code 401 Unauthorized", ErrAuth},

		// Stale endpoint
		{"CF 530", "failed to connect to tunnel: status code 530", ErrStaleEndpoint},
		{"no such host", "lookup foo.trycloudflare.com: no such host", ErrStaleEndpoint},
		{"connection refused", "connection refused", ErrStaleEndpoint},
		{"env stopped/expired", "environment unreachable — it may have stopped or the tunnel expired",
			ErrStaleEndpoint},

		// Transient
		{"unavailable", "rpc error: code = Unavailable desc = server", ErrTransient},
		{"502", "status code 502 Bad Gateway", ErrTransient},
		{"i/o timeout", "read tcp: i/o timeout", ErrTransient},

		// Default fallthrough — assume stale.
		{"unknown falls through to stale", "some novel error we have no pattern for", ErrStaleEndpoint},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyError(tc.msg); got != tc.want {
				t.Errorf("classifyError(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}
