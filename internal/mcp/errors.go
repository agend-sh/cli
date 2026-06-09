package mcp

import "strings"

// ErrorCategory classifies connection errors to determine retry strategy.
type ErrorCategory int

const (
	ErrTransient     ErrorCategory = iota // retry same endpoint with backoff
	ErrStaleEndpoint                      // re-resolve via control plane API
	ErrAuth                               // re-authenticate, then retry
	ErrFatal                              // return error to caller, do not retry
)

func classifyError(errText string) ErrorCategory {
	t := strings.ToLower(errText)

	// Client-side cancellation — our deadline expired, not a connection issue.
	// Return the error directly, don't retry or reconnect.
	if strings.Contains(t, "context canceled") || strings.Contains(t, "context deadline exceeded") {
		return ErrFatal
	}

	// Fatal — env is gone or access denied
	if strings.Contains(t, "not found") && strings.Contains(t, "404") {
		return ErrFatal
	}
	if strings.Contains(t, "forbidden") || strings.Contains(t, "403") {
		return ErrFatal
	}
	// port_expose config errors — retries will never fix these. Return
	// immediately so the LLM caller sees the actionable daemon message
	// instead of a generic "environment unreachable" after 30s of retrying.
	if strings.Contains(t, "does not belong to cloudflare zone") {
		return ErrFatal
	}
	if strings.Contains(t, "already bound: cname points to") {
		return ErrFatal
	}

	// Auth — token/session expired
	if strings.Contains(t, "unauthenticated") {
		return ErrAuth
	}
	if strings.Contains(t, "401") && !strings.Contains(t, "websocket") {
		return ErrAuth
	}

	// Stale endpoint — tunnel is dead, need fresh endpoint
	if strings.Contains(t, "status code 530") {
		return ErrStaleEndpoint
	}
	if strings.Contains(t, "no such host") {
		return ErrStaleEndpoint
	}
	if strings.Contains(t, "connection refused") {
		return ErrStaleEndpoint
	}
	if strings.Contains(t, "tunnel connection") && strings.Contains(t, "failed") {
		return ErrStaleEndpoint
	}
	// Both "may be stopped" and "may have stopped" variants seen in the wild.
	if strings.Contains(t, "may be stopped or expired") ||
		strings.Contains(t, "may have stopped or the tunnel expired") {
		return ErrStaleEndpoint
	}

	// Transient — tunnel flap, tableflip, brief outage
	if strings.Contains(t, "unavailable") {
		return ErrTransient
	}
	if strings.Contains(t, "status code 502") || strings.Contains(t, "status code 503") {
		return ErrTransient
	}
	if strings.Contains(t, "i/o timeout") {
		return ErrTransient
	}
	if strings.Contains(t, "connection reset") {
		return ErrTransient
	}
	if strings.Contains(t, "deadline exceeded") {
		return ErrTransient
	}
	if strings.Contains(t, "unreachable") {
		return ErrTransient
	}

	// Default: assume stale — triggers re-resolve which is the safest recovery
	return ErrStaleEndpoint
}
