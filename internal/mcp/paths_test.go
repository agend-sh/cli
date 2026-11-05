package mcp

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newTestLocalFiles(t *testing.T, root, access string) *localFiles {
	t.Helper()
	t.Setenv("AGEND_LOCAL_ROOT", root)
	t.Setenv("AGEND_LOCAL_ACCESS", access)
	files, err := newLocalFiles()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = files.Close() })
	return files
}

func TestLocalTargetConfinesPaths(t *testing.T) {
	root := t.TempDir()
	files := newTestLocalFiles(t, root, "confined")
	outside := filepath.Join(filepath.Dir(root), "outside-secret")

	tests := []struct {
		name    string
		path    string
		wantErr string
	}{
		{"relative inside root", "ok.txt", ""},
		{"absolute inside root", filepath.Join(root, "ok.txt"), ""},
		{"new file inside root", "new/sub/file.bin", ""},
		{"empty", "", "required"},
		{"root itself", root, "not a file"},
		{"absolute outside root", outside, "outside"},
		{"traversal escape", "../outside-secret", "outside"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			target, err := files.target(tc.path, false)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("target(%q) error: %v", tc.path, err)
				}
				if target.display != root && !strings.HasPrefix(target.display, root+string(filepath.Separator)) {
					t.Errorf("display path %q escapes root %q", target.display, root)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("target(%q) = %#v, %v; want error containing %q", tc.path, target, err, tc.wantErr)
			}
		})
	}
}

func TestOpenUploadRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "id_rsa")
	if err := os.WriteFile(secret, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "sneaky")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "sneakydir")); err != nil {
		t.Fatal(err)
	}

	files := newTestLocalFiles(t, root, "confined")
	for _, path := range []string{"sneaky", filepath.Join("sneakydir", "id_rsa")} {
		if file, _, err := files.openUpload(path); err == nil {
			file.Close()
			t.Fatalf("openUpload(%q) followed a symlink outside the root", path)
		}
	}
}

func TestPinnedRootSurvivesPathReplacement(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "root")
	movedPath := filepath.Join(parent, "moved")
	outside := filepath.Join(parent, "outside")
	for _, dir := range []string{rootPath, outside} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(rootPath, "value"), []byte("inside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "value"), []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}

	files := newTestLocalFiles(t, rootPath, "confined")
	if err := os.Rename(rootPath, movedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, rootPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	file, _, err := files.openUpload("value")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got, err := io.ReadAll(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "inside" {
		t.Fatalf("pinned root read %q, want original root content", got)
	}
}

func TestStagedDownloadReplacesSymlinkWithoutFollowingIt(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("do not replace"), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "result")
	if err := os.Symlink(outside, target); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	files := newTestLocalFiles(t, root, "confined")
	staged, err := files.createDownload("result")
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Abort()
	if _, err := staged.File().Write([]byte("download")); err != nil {
		t.Fatal(err)
	}
	if err := staged.Commit(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "download" {
		t.Fatalf("target = %q, want downloaded content", got)
	}
	outsideData, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(outsideData) != "do not replace" {
		t.Fatalf("outside file changed to %q", outsideData)
	}
}

func TestReadOnlyAllowsUploadButBlocksDownload(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "input"), []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	files := newTestLocalFiles(t, root, "read-only")
	file, _, err := files.openUpload("input")
	if err != nil {
		t.Fatalf("read-only upload source: %v", err)
	}
	file.Close()
	if _, err := files.createDownload("output"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("createDownload error = %v, want read-only rejection", err)
	}
}

func TestUnrestrictedOptInAllowsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(outside, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	files := newTestLocalFiles(t, root, "unrestricted")
	file, display, err := files.openUpload(outside)
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	if display != outside {
		t.Fatalf("display = %q, want %q", display, outside)
	}
}

func TestInvalidLocalAccessModeFailsClosed(t *testing.T) {
	t.Setenv("AGEND_LOCAL_ROOT", t.TempDir())
	t.Setenv("AGEND_LOCAL_ACCESS", "maybe")
	if _, err := newLocalFiles(); err == nil || !strings.Contains(err.Error(), "invalid AGEND_LOCAL_ACCESS") {
		t.Fatalf("newLocalFiles error = %v", err)
	}
}

func TestDangerousImplicitRoot(t *testing.T) {
	root := filepath.VolumeName(string(filepath.Separator)) + string(filepath.Separator)
	if !dangerousImplicitRoot(root) {
		t.Errorf("filesystem root %q should be dangerous", root)
	}
	home, err := os.UserHomeDir()
	if err == nil && !dangerousImplicitRoot(home) {
		t.Errorf("home %q should be dangerous", home)
	}
	if runtime.GOOS != "windows" && dangerousImplicitRoot(t.TempDir()) {
		t.Error("temporary directory should not be dangerous")
	}
}
