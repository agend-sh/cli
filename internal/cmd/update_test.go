package cmd

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestArchiveName(t *testing.T) {
	got := archiveName("1.2.3")
	wantExt := ".tar.gz"
	if runtime.GOOS == "windows" {
		wantExt = ".zip"
	}
	want := fmt.Sprintf("agend-1.2.3-%s-%s%s", runtime.GOOS, runtime.GOARCH, wantExt)
	if got != want {
		t.Errorf("archiveName = %q, want %q", got, want)
	}
}

func TestBinaryName(t *testing.T) {
	want := "agend"
	if runtime.GOOS == "windows" {
		want = "agend.exe"
	}
	if got := binaryName(); got != want {
		t.Errorf("binaryName = %q, want %q", got, want)
	}
}

func makeZip(t *testing.T, dir, member string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, "test.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	w, err := zw.Create(member)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func makeTarGz(t *testing.T, dir, member string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, "test.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: member, Mode: 0755, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// extractBinary must dispatch on the archive extension, find the binary by
// name (flat or nested), and place the temp file inside destDir so the final
// rename never crosses volumes.
func TestExtractBinary(t *testing.T) {
	content := []byte("fake binary bytes")
	destDir := t.TempDir()

	cases := []struct {
		name    string
		archive func(t *testing.T, dir string) string
	}{
		{"zip flat", func(t *testing.T, dir string) string {
			return makeZip(t, dir, "agend.exe", content)
		}},
		{"zip nested", func(t *testing.T, dir string) string {
			return makeZip(t, dir, "agend-1.2.3-windows-amd64/agend.exe", content)
		}},
		{"targz flat", func(t *testing.T, dir string) string {
			return makeTarGz(t, dir, "agend.exe", content)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := tc.archive(t, t.TempDir())
			out, err := extractBinary(archive, "agend.exe", destDir)
			if err != nil {
				t.Fatalf("extractBinary: %v", err)
			}
			defer os.Remove(out)
			if filepath.Dir(out) != destDir {
				t.Errorf("extracted to %q, want inside %q", out, destDir)
			}
			got, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, content) {
				t.Errorf("extracted content mismatch")
			}
		})
	}
}

func TestExtractBinaryMissingMember(t *testing.T) {
	archive := makeZip(t, t.TempDir(), "somethingelse.exe", []byte("x"))
	if _, err := extractBinary(archive, "agend.exe", t.TempDir()); err == nil {
		t.Fatal("expected error for missing archive member")
	}
}

// The spooled temp file must keep the asset's extension — extractBinary
// dispatches zip vs tar.gz on it.
func TestDownloadAndVerifyKeepsExtension(t *testing.T) {
	data := []byte("archive bytes")
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])

	for _, asset := range []string{"agend-1.0.0-windows-amd64.zip", "agend-1.0.0-linux-amd64.tar.gz"} {
		path, err := downloadAndVerify(bytes.NewReader(data), hexSum, asset)
		if path != "" {
			defer os.Remove(path)
		}
		if err != nil {
			t.Fatalf("downloadAndVerify(%s): %v", asset, err)
		}
		wantExt := ".tar.gz"
		if strings.HasSuffix(asset, ".zip") {
			wantExt = ".zip"
		}
		if !strings.HasSuffix(path, wantExt) {
			t.Errorf("temp file %q should keep extension %s of asset %s", path, wantExt, asset)
		}
	}

	// Fail closed on checksum mismatch.
	path, err := downloadAndVerify(bytes.NewReader(data), strings.Repeat("0", 64), "x.tar.gz")
	if path != "" {
		defer os.Remove(path)
	}
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
}
