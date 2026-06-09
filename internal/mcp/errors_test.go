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
		{"404 not found", "rpc error: code = NotFound desc = 404 not found", ErrFatal},
		{"403 forbidden", "rpc error: code = PermissionDenied desc = 403 forbidden", ErrFatal},

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
