package mcp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Local filesystem access policy for MCP file tools.
//
// file_download and file_upload take local paths from tool arguments, which
// ultimately come from an LLM that may be prompt-injected by a hostile
// remote environment. Without confinement, the environment could trick the
// agent into reading ~/.ssh/id_rsa off the client machine or overwriting
// ~/.bashrc. Local paths are therefore confined to the MCP server's working
// directory (where the user launched their agent), or to AGEND_LOCAL_ROOT
// if set.

// localRoot returns the directory MCP file tools may read/write under,
// with symlinks resolved.
func localRoot() (string, error) {
	root := os.Getenv("AGEND_LOCAL_ROOT")
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("determine working directory: %w", err)
		}
		root = cwd
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve local root %s: %w", root, err)
	}
	return resolved, nil
}

// resolveLocalPath validates a local path from tool arguments and returns
// its absolute, symlink-resolved form. The resolved path must stay within
// localRoot(). Symlinks anywhere in the path (including the final
// component, if it exists) are resolved before the containment check, so a
// link pointing outside the root is rejected.
func resolveLocalPath(path string) (string, error) {
	if path == "" {
		return "", errors.New("local_path is required")
	}

	root, err := localRoot()
	if err != nil {
		return "", err
	}

	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(root, abs)
	}
	abs = filepath.Clean(abs)

	// Resolve symlinks in the longest existing prefix, then re-append the
	// not-yet-existing remainder (e.g. a download target that doesn't
	// exist yet).
	resolved, err := resolveExistingPrefix(abs)
	if err != nil {
		return "", err
	}

	// Roots that already end in a separator (filesystem root "/", Windows
	// drive roots like "C:\") must not get a second one appended, or the
	// prefix never matches and every path is rejected.
	rootPrefix := root
	if !strings.HasSuffix(rootPrefix, string(filepath.Separator)) {
		rootPrefix += string(filepath.Separator)
	}
	if resolved != root && !strings.HasPrefix(resolved, rootPrefix) {
		return "", fmt.Errorf("local_path %s is outside the allowed directory %s (set AGEND_LOCAL_ROOT to change it)", path, root)
	}
	return resolved, nil
}

// resolveExistingPrefix resolves symlinks in the longest existing ancestor
// of path and joins back the trailing components that don't exist yet.
func resolveExistingPrefix(abs string) (string, error) {
	var tail []string
	cur := abs
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, tail[i])
			}
			return resolved, nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("resolve %s: %w", cur, err)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("resolve %s: no existing ancestor", abs)
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

// createLocalFile opens path for writing, refusing to write through a
// symlink or onto anything that isn't a regular file.
func createLocalFile(path string) (*os.File, error) {
	if fi, err := os.Lstat(path); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("local_path %s is a symlink — refusing to write through it", path)
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("local_path %s exists and is not a regular file", path)
		}
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
}
