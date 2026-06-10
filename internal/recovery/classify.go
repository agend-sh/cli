// Package recovery holds the single source of truth for classifying
// connection errors into retry strategies, shared by the MCP bridge
// (internal/mcp) and the direct CLI commands (internal/cmd). Previously each
// had its own string-matching classifier and the two had already diverged.
package recovery

import (
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Category determines how a failed call should be recovered.
type Category int

const (
	Transient     Category = iota // retry same endpoint with backoff
	StaleEndpoint                 // re-resolve endpoint via control plane
	Auth                          // re-authenticate, then retry
	Fatal                         // surface to caller, do not retry
)

// Classify maps an error to a recovery Category. When the error carries a gRPC
// status code it is classified on the code (robust); otherwise it falls back
// to ClassifyText on the message.
func Classify(err error) Category {
	if err == nil {
		return Fatal
	}
	if st, ok := status.FromError(err); ok && st.Code() != codes.Unknown {
		switch st.Code() {
		case codes.Unauthenticated:
			return Auth
		case codes.PermissionDenied, codes.NotFound, codes.InvalidArgument,
			codes.FailedPrecondition, codes.Canceled:
			return Fatal
		case codes.Unavailable:
			return Transient
		case codes.DeadlineExceeded:
			return Transient
		}
	}
	return ClassifyText(err.Error())
}

// ClassifyText maps an error message to a recovery Category. Used by the MCP
// bridge, whose tool functions surface errors as formatted text rather than
// typed errors.
func ClassifyText(msg string) Category {
	t := strings.ToLower(msg)

	// Client-side cancellation — our deadline expired, not a connection issue.
	if strings.Contains(t, "context canceled") || strings.Contains(t, "context deadline exceeded") {
		return Fatal
	}

	// Fatal — env gone, access denied, or a config error retries can't fix.
	if strings.Contains(t, "not found") && strings.Contains(t, "404") {
		return Fatal
	}
	if strings.Contains(t, "forbidden") || strings.Contains(t, "403") {
		return Fatal
	}
	if strings.Contains(t, "does not belong to cloudflare zone") {
		return Fatal
	}
	if strings.Contains(t, "already bound: cname points to") {
		return Fatal
	}

	// Auth — token/session expired.
	if strings.Contains(t, "unauthenticated") {
		return Auth
	}
	if strings.Contains(t, "invalid session token") {
		return Auth
	}
	if strings.Contains(t, "401") && !strings.Contains(t, "websocket") {
		return Auth
	}

	// Stale endpoint — tunnel dead, need a fresh endpoint.
	if strings.Contains(t, "status code 530") ||
		strings.Contains(t, "no such host") ||
		strings.Contains(t, "connection refused") ||
		strings.Contains(t, "environment unreachable") ||
		(strings.Contains(t, "tunnel connection") && strings.Contains(t, "failed")) ||
		strings.Contains(t, "may be stopped or expired") ||
		strings.Contains(t, "may have stopped or the tunnel expired") {
		return StaleEndpoint
	}

	// Transient — tunnel flap, tableflip, brief outage.
	if strings.Contains(t, "unavailable") ||
		strings.Contains(t, "status code 502") || strings.Contains(t, "status code 503") ||
		strings.Contains(t, "i/o timeout") ||
		strings.Contains(t, "connection reset") ||
		strings.Contains(t, "deadline exceeded") ||
		strings.Contains(t, "unreachable") {
		return Transient
	}

	// Default: assume stale — re-resolve is the safest recovery, and the retry
	// loop only re-runs the call itself for idempotent operations.
	return StaleEndpoint
}

// nonIdempotentExempt is the read-only / idempotent tool allowlist. Everything
// not listed is treated as side-effecting and must NOT be transparently
// re-executed after it may have already run (e.g. a "connection reset" that
// arrives after the daemon already started a shell_exec). Default-deny is the
// safe choice — a wrongly-retried mutation is worse than a surfaced error.
var idempotentTools = map[string]bool{
	"list_environments": true,
	"env_status":        true,
	"env_stats":         true,
	"port_list":         true,
	"file_download":     true,
	"shell_task_output": true,
	"reload_config":     true,
}

// IsIdempotent reports whether a tool/op is safe to transparently re-execute.
func IsIdempotent(tool string) bool {
	return idempotentTools[tool]
}
