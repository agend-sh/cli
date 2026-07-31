package mcp

import (
	"strings"

	"github.com/agend-sh/cli/internal/recovery"
)

// ErrorCategory classifies connection errors to determine retry strategy.
// It mirrors recovery.Category so existing call sites keep their names; the
// classification logic itself lives in internal/recovery (single source of
// truth shared with the direct CLI commands).
type ErrorCategory = recovery.Category

const (
	ErrTransient     = recovery.Transient
	ErrStaleEndpoint = recovery.StaleEndpoint
	ErrAuth          = recovery.Auth
	ErrFatal         = recovery.Fatal
)

func classifyError(errText string) ErrorCategory {
	lower := strings.ToLower(errText)

	// These errors are produced by the MCP client itself, not by the
	// environment transport. They must never cause endpoint recovery: doing so
	// hides the actionable local error and can stall an otherwise healthy tool
	// call for the full tunnel warm-up window.
	switch {
	case strings.Contains(lower, "local_path ") &&
		(strings.Contains(lower, "outside the allowed directory") ||
			strings.Contains(lower, "is required") ||
			strings.Contains(lower, "is a symlink") ||
			strings.Contains(lower, "is not a regular file")):
		return ErrFatal
	case strings.HasPrefix(lower, "download failed: determine working directory:"),
		strings.HasPrefix(lower, "download failed: resolve local root "),
		strings.HasPrefix(lower, "download failed: resolve "),
		strings.HasPrefix(lower, "upload failed: determine working directory:"),
		strings.HasPrefix(lower, "upload failed: resolve local root "),
		strings.HasPrefix(lower, "upload failed: resolve "),
		strings.HasPrefix(lower, "create local file:"),
		strings.HasPrefix(lower, "read local file:"),
		strings.HasPrefix(lower, "write:"):
		return ErrFatal
	case strings.HasPrefix(lower, "download failed: checksum mismatch"):
		return ErrTransient
	}

	return recovery.ClassifyText(errText)
}
