//go:build windows

package cmd

import "errors"

// reExec is unavailable on Windows (no execve). The binary has already been
// swapped on disk, so the new version takes effect on the next launch; the
// caller reports that to the user.
func reExec(path string, args []string) error {
	return errors.New("re-exec not supported on windows")
}
