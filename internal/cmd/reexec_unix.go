//go:build !windows

package cmd

import (
	"os"
	"syscall"
)

// reExec replaces the current process image with the freshly-installed binary,
// continuing the same invocation. syscall.Exec keeps the PID and all inherited
// file descriptors (stdin/stdout/stderr) — so an `agend mcp` stdio session
// stays wired to its client with no restart, dropped connection, or lost state
// (the MCP handshake happens only after exec returns into the new binary).
// AGEND_SELF_UPDATED guards against a re-exec loop. path is the freshly
// installed binary (resolved before the swap, since os.Executable() afterward
// points at the unlinked old inode).
func reExec(path string, args []string) error {
	env := append(os.Environ(), "AGEND_SELF_UPDATED=1")
	return syscall.Exec(path, args, env)
}
