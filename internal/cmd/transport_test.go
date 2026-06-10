package cmd

import (
	"errors"
	"testing"
)

func TestClassifyErr(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want errorCategory
	}{
		// errFatal — client-side cancellation
		{"context canceled", "rpc error: code = Canceled desc = context canceled", errFatal},
		{"context deadline exceeded", "context deadline exceeded", errFatal},

		// errFatal — gRPC 4xx
		{"404 not found", "rpc error: code = NotFound desc = 404 not found", errFatal},
		{"403 forbidden", "rpc error: code = PermissionDenied desc = 403 forbidden", errFatal},

		// errFatal — port_expose config errors (daemon v0.2.6 surface)
		{"hostname not in zone",
			"rpc error: code = Unknown desc = createDNS: app.example.com.br does not belong to Cloudflare zone agend.sh (zone id ...); pass cf_zone_id",
			errFatal},
		{"DNS bound elsewhere",
			"rpc error: code = Unknown desc = app.example.com.br already bound: CNAME points to foo.cfargotunnel.com (not our tunnel bar.cfargotunnel.com); delete...",
			errFatal},

		// errAuth
		{"unauthenticated", "rpc error: code = Unauthenticated desc = unauthenticated", errAuth},
		{"invalid session token", "invalid session token", errAuth},
		{"401 HTTP (non-websocket)", "status code 401 Unauthorized", errAuth},

		// errStaleEndpoint
		{"CF error 530", "failed to connect to tunnel: status code 530", errStaleEndpoint},
		{"no such host", "dial tcp: lookup foo.trycloudflare.com: no such host", errStaleEndpoint},
		{"connection refused", "dial tcp: connection refused", errStaleEndpoint},
		{"environment unreachable", "environment unreachable — it may have stopped", errStaleEndpoint},

		// errTransient
		{"gRPC Unavailable", "rpc error: code = Unavailable desc = server", errTransient},
		{"502", "status code 502 Bad Gateway", errTransient},
		{"503", "status code 503 Service Unavailable", errTransient},
		{"i/o timeout", "read tcp ...: i/o timeout", errTransient},

		// Default fallthrough — assume stale so a mystery error at least triggers
		// an endpoint re-resolve before giving up.
		{"unknown error falls through to stale",
			"some bizarre error we've never seen", errStaleEndpoint},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyErr(errors.New(tc.msg)); got != tc.want {
				t.Errorf("classifyErr(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}
