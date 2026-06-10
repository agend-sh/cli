package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveLocalPath(t *testing.T) {
	root := t.TempDir()
	// Resolve the temp dir itself — on macOS /tmp is a symlink.
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGEND_LOCAL_ROOT", root)

	outside := t.TempDir()
	secret := filepath.Join(outside, "id_rsa")
	if err := os.WriteFile(secret, []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(root, "ok.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(root, "sneaky")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "sneakydir")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		path    string
		wantErr string // empty = expect success
	}{
		{"relative inside root", "ok.txt", ""},
		{"absolute inside root", filepath.Join(root, "ok.txt"), ""},
		{"new file inside root", "new/sub/file.bin", ""},
		{"empty", "", "required"},
		{"absolute outside root", secret, "outside"},
		{"traversal escape", "../" + filepath.Base(outside) + "/id_rsa", "outside"},
		{"symlink file pointing outside", "sneaky", "outside"},
		{"symlink dir pointing outside", "sneakydir/id_rsa", "outside"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveLocalPath(tc.path)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("resolveLocalPath(%q) error: %v", tc.path, err)
				}
				if got != root && !strings.HasPrefix(got, root+string(filepath.Separator)) {
					t.Errorf("resolved path %q escapes root %q", got, root)
				}
				return
			}
			if err == nil {
				t.Fatalf("resolveLocalPath(%q) = %q, want error containing %q", tc.path, got, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestCreateLocalFileRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if _, err := createLocalFile(link); err == nil {
		t.Error("createLocalFile followed a symlink")
	}
	f, err := createLocalFile(filepath.Join(dir, "fresh"))
	if err != nil {
		t.Fatalf("createLocalFile on fresh path: %v", err)
	}
	f.Close()
}
