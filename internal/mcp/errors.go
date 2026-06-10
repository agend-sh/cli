package mcp

import "github.com/agend-sh/cli/internal/recovery"

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
	return recovery.ClassifyText(errText)
}
