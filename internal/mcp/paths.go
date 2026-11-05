package mcp

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Local filesystem access policy for MCP file tools.
//
// Tool arguments ultimately come from an LLM that may be prompt-injected by a
// hostile remote environment. The default policy confines both reads and
// writes to the directory in which the MCP server started (or
// AGEND_LOCAL_ROOT). AGEND_LOCAL_ACCESS=read-only additionally blocks local
// writes. AGEND_LOCAL_ACCESS=unrestricted is an explicit, warned escape hatch.
//
// Confined operations use os.Root. On Unix, Go implements Root operations by
// walking from a pinned directory descriptor with openat and O_NOFOLLOW. On
// Windows it uses root-relative handles and rejects reparse-point escapes. The
// file descriptor returned by Root.Open is the object we operate on; no
// validate-then-reopen path race remains.

type localAccessMode uint8

const (
	localAccessConfined localAccessMode = iota
	localAccessReadOnly
	localAccessUnrestricted
)

type localFiles struct {
	mode     localAccessMode
	root     *os.Root
	rootPath string
	cwd      string
}

type localTarget struct {
	rel     string
	display string
}

func newLocalFiles() (*localFiles, error) {
	mode := localAccessConfined
	switch access := strings.TrimSpace(os.Getenv("AGEND_LOCAL_ACCESS")); access {
	case "", "confined":
	case "read-only":
		mode = localAccessReadOnly
	case "unrestricted":
		mode = localAccessUnrestricted
	default:
		return nil, fmt.Errorf("invalid AGEND_LOCAL_ACCESS %q (use confined, read-only, or unrestricted)", access)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("determine working directory: %w", err)
	}
	cwd, err = filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve working directory: %w", err)
	}

	files := &localFiles{mode: mode, cwd: cwd}
	if mode == localAccessUnrestricted {
		log.Printf("WARNING: AGEND_LOCAL_ACCESS=unrestricted lets MCP file tools read or replace any file accessible to this user; use confined or read-only whenever possible")
		return files, nil
	}

	configuredRoot := strings.TrimSpace(os.Getenv("AGEND_LOCAL_ROOT"))
	rootPath := configuredRoot
	if rootPath == "" {
		rootPath = cwd
		if dangerousImplicitRoot(rootPath) {
			return nil, fmt.Errorf("refusing implicit MCP local root %s; set AGEND_LOCAL_ROOT to an explicit narrow directory", rootPath)
		}
	}
	rootPath, err = filepath.Abs(rootPath)
	if err != nil {
		return nil, fmt.Errorf("resolve local root %s: %w", rootPath, err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open local root %s: %w", rootPath, err)
	}
	files.root = root
	files.rootPath = rootPath
	return files, nil
}

func dangerousImplicitRoot(path string) bool {
	clean := filepath.Clean(path)
	volumeRoot := filepath.VolumeName(clean) + string(filepath.Separator)
	if clean == volumeRoot {
		return true
	}
	home, err := os.UserHomeDir()
	return err == nil && samePath(clean, filepath.Clean(home))
}

func samePath(a, b string) bool {
	if filepath.Separator == '\\' {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func (f *localFiles) Close() error {
	if f == nil || f.root == nil {
		return nil
	}
	return f.root.Close()
}

// target validates a local_path and converts it to a path relative to the
// pinned root. write is true for file_download, which modifies the local
// filesystem; read-only mode still permits file_upload.
func (f *localFiles) target(path string, write bool) (localTarget, error) {
	if path == "" {
		return localTarget{}, errors.New("local_path is required")
	}
	if f == nil {
		return localTarget{}, errors.New("local filesystem policy is unavailable")
	}
	if write && f.mode == localAccessReadOnly {
		return localTarget{}, errors.New("local writes are disabled by AGEND_LOCAL_ACCESS=read-only")
	}

	if f.mode == localAccessUnrestricted {
		abs := path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(f.cwd, abs)
		}
		abs, err := filepath.Abs(abs)
		if err != nil {
			return localTarget{}, fmt.Errorf("resolve local_path %s: %w", path, err)
		}
		if filepath.Clean(abs) == filepath.VolumeName(abs)+string(filepath.Separator) {
			return localTarget{}, fmt.Errorf("local_path %s names a directory, not a file", path)
		}
		return localTarget{rel: filepath.Clean(abs), display: filepath.Clean(abs)}, nil
	}

	rel := path
	if filepath.IsAbs(path) {
		var err error
		rel, err = filepath.Rel(f.rootPath, path)
		if err != nil {
			return localTarget{}, fmt.Errorf("resolve local_path %s relative to %s: %w", path, f.rootPath, err)
		}
	}
	rel = filepath.Clean(rel)
	if rel == "." {
		return localTarget{}, fmt.Errorf("local_path %s names the allowed directory, not a file", path)
	}
	if !filepath.IsLocal(rel) {
		return localTarget{}, fmt.Errorf("local_path %s is outside the allowed directory %s (set AGEND_LOCAL_ROOT to change it, or explicitly opt in with AGEND_LOCAL_ACCESS=unrestricted)", path, f.rootPath)
	}
	return localTarget{rel: rel, display: filepath.Join(f.rootPath, rel)}, nil
}

func (f *localFiles) openUpload(path string) (*os.File, string, error) {
	target, err := f.target(path, false)
	if err != nil {
		return nil, "", err
	}
	var file *os.File
	if f.mode == localAccessUnrestricted {
		file, err = os.Open(target.rel)
	} else {
		file, err = f.root.Open(target.rel)
	}
	if err != nil {
		return nil, "", fmt.Errorf("open local_path %s: %w", path, err)
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, "", fmt.Errorf("inspect local_path %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, "", fmt.Errorf("local_path %s is not a regular file", path)
	}
	return file, target.display, nil
}

// stagedLocalFile writes a download to an unpredictable sibling and atomically
// renames it into place only after the complete checksum verifies. This avoids
// partial output and never follows a destination symlink.
type stagedLocalFile struct {
	policy  *localFiles
	file    *os.File
	target  localTarget
	tempRel string
	done    bool
}

func (f *localFiles) createDownload(path string) (*stagedLocalFile, error) {
	target, err := f.target(path, true)
	if err != nil {
		return nil, err
	}

	parent := filepath.Dir(target.rel)
	base := filepath.Base(target.rel)
	for attempt := 0; attempt < 16; attempt++ {
		suffix, err := randomSuffix()
		if err != nil {
			return nil, fmt.Errorf("generate local temporary name: %w", err)
		}
		tempRel := filepath.Join(parent, "."+base+".agend-"+suffix)
		var file *os.File
		if f.mode == localAccessUnrestricted {
			file, err = os.OpenFile(tempRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		} else {
			file, err = f.root.OpenFile(tempRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("create staged local_path %s: %w", path, err)
		}
		return &stagedLocalFile{policy: f, file: file, target: target, tempRel: tempRel}, nil
	}
	return nil, fmt.Errorf("create staged local_path %s: temporary name collisions", path)
}

func randomSuffix() (string, error) {
	var raw [12]byte
	if _, err := io.ReadFull(rand.Reader, raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func (s *stagedLocalFile) File() *os.File { return s.file }

func (s *stagedLocalFile) DisplayPath() string { return s.target.display }

func (s *stagedLocalFile) Commit() error {
	if s.done {
		return errors.New("staged local file is already closed")
	}
	if err := s.file.Sync(); err != nil {
		return fmt.Errorf("sync staged local file: %w", err)
	}
	if err := s.file.Close(); err != nil {
		return fmt.Errorf("close staged local file: %w", err)
	}
	var err error
	if s.policy.mode == localAccessUnrestricted {
		err = os.Rename(s.tempRel, s.target.rel)
	} else {
		err = s.policy.root.Rename(s.tempRel, s.target.rel)
	}
	if err != nil {
		return fmt.Errorf("publish local_path %s: %w", s.target.display, err)
	}
	s.done = true
	return nil
}

func (s *stagedLocalFile) Abort() {
	if s == nil || s.done {
		return
	}
	_ = s.file.Close()
	if s.policy.mode == localAccessUnrestricted {
		_ = os.Remove(s.tempRel)
	} else {
		_ = s.policy.root.Remove(s.tempRel)
	}
	s.done = true
}
