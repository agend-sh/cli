package cmd

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const (
	releasesURL = "https://api.github.com/repos/agend-sh/cli/releases/latest"
	downloadURL = "https://github.com/agend-sh/cli/releases/download"
)

type githubRelease struct {
	TagName string `json:"tag_name"`
}

// archiveName returns the release asset name for this platform, matching
// .goreleaser.yaml: tar.gz everywhere except windows, which ships zip.
func archiveName(version string) string {
	ext := "tar.gz"
	if runtime.GOOS == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("agend-%s-%s-%s.%s", version, runtime.GOOS, runtime.GOARCH, ext)
}

// binaryName is the executable's name inside the release archive.
func binaryName() string {
	if runtime.GOOS == "windows" {
		return "agend.exe"
	}
	return "agend"
}

// reinstallHint tells the user how to reinstall from scratch when the
// in-place swap fails.
func reinstallHint() string {
	if runtime.GOOS == "windows" {
		return "try: irm agend.sh/i.ps1 | iex"
	}
	return "try: curl -fsSL agend.sh/i | sh"
}

func newUpdateCmd(currentVersion string) *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "update",
		Short: "Update agend to the latest version",
		Long: `Update agend to the latest version from GitHub Releases.

If agend is currently running as an MCP server (e.g. serving Claude, Cursor,
or another AI agent), the update replaces the binary on disk. The running
process continues with the old version until the agent restarts it.

On the next launch, the new version takes effect automatically.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Clean up stale .old binary from a previous update
			cleanupOldBinary()

			client := &http.Client{Timeout: 30 * time.Second}

			// Fetch latest release
			fmt.Print("Checking for updates... ")
			tag, err := fetchLatestTag(client)
			if err != nil {
				return err
			}

			latest := strings.TrimPrefix(tag, "v")
			current := strings.TrimPrefix(currentVersion, "v")
			if latest == current && !force {
				fmt.Printf("already up to date (%s)\n", currentVersion)
				return nil
			}

			fmt.Printf("found %s (current: %s)\n", tag, currentVersion)
			fmt.Printf("Downloading %s... ", archiveName(latest))
			if err := installVersion(client, tag); err != nil {
				return err
			}

			fmt.Println("done!")
			fmt.Printf("Updated to %s\n", tag)
			fmt.Println("Running MCP servers will use the new version on next restart.")
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "re-download even if already up to date")

	return cmd
}

// fetchLatestTag returns the latest published release tag (e.g. "v1.1.4").
func fetchLatestTag(client *http.Client) (string, error) {
	req, err := http.NewRequest("GET", releasesURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to check for updates: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}
	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("failed to parse release: %w", err)
	}
	if release.TagName == "" {
		return "", fmt.Errorf("no tag_name in latest release")
	}
	return release.TagName, nil
}

// installVersion downloads the given release tag for this platform, verifies
// its checksum (fail-closed), and atomically swaps it in for the running
// binary. It prints nothing — callers handle UX.
func installVersion(client *http.Client, tag string) error {
	latest := strings.TrimPrefix(tag, "v")
	asset := archiveName(latest)

	expectedSum, err := fetchChecksum(client, tag, asset)
	if err != nil {
		return fmt.Errorf("fetch checksums: %w", err)
	}

	url := fmt.Sprintf("%s/%s/%s", downloadURL, tag, asset)
	dlResp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer dlResp.Body.Close()
	if dlResp.StatusCode != 200 {
		return fmt.Errorf("download failed: HTTP %d", dlResp.StatusCode)
	}

	// Spool to a temp file so the whole archive is checksummed before anything
	// is extracted or installed. The temp name keeps the asset's extension —
	// extractBinary dispatches zip vs tar.gz on it.
	tmpArchive, err := downloadAndVerify(dlResp.Body, expectedSum, asset)
	if tmpArchive != "" {
		defer os.Remove(tmpArchive)
	}
	if err != nil {
		return err
	}

	// Extract into the install directory, not the system temp dir: the final
	// os.Rename must stay on one volume (cross-volume renames fail — always on
	// Windows when agend lives off C:, and on Linux when /tmp is tmpfs).
	destDir, err := installTargetDir()
	if err != nil {
		return err
	}

	tmpBinary, err := extractBinary(tmpArchive, binaryName(), destDir)
	if err != nil {
		return fmt.Errorf("extract failed: %w", err)
	}
	defer os.Remove(tmpBinary)

	if err := os.Chmod(tmpBinary, 0755); err != nil {
		return err
	}

	// Replace the current binary via rename-swap — safe even while another
	// agend process is running (the OS keeps the old inode open via its fd).
	return swapBinary(tmpBinary)
}

// installTargetDir returns the directory of the (symlink-resolved) running
// binary — where the update will be installed.
func installTargetDir() (string, error) {
	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cannot find current binary: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return "", fmt.Errorf("resolve binary path: %w", err)
	}
	return filepath.Dir(execPath), nil
}

// fetchChecksum downloads checksums.txt from the release and returns the
// expected sha256 for the named archive. A missing checksums file or a
// missing entry is an error — the update never proceeds unverified.
func fetchChecksum(client *http.Client, tag, archiveName string) (string, error) {
	url := fmt.Sprintf("%s/%s/checksums.txt", downloadURL, tag)
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("checksums.txt: HTTP %d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[1] == archiveName {
			if len(fields[0]) != 64 {
				return "", fmt.Errorf("malformed checksum for %s", archiveName)
			}
			return strings.ToLower(fields[0]), nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("no checksum entry for %s in checksums.txt", archiveName)
}

// downloadAndVerify spools the archive to a temp file while hashing it,
// then compares against the expected sha256. Returns the temp file path;
// on mismatch the file path is still returned (so the caller can clean up)
// along with the error.
func downloadAndVerify(r io.Reader, expectedSum, assetName string) (string, error) {
	ext := ".tar.gz"
	if strings.HasSuffix(assetName, ".zip") {
		ext = ".zip"
	}
	tmp, err := os.CreateTemp("", "agend-archive-*"+ext)
	if err != nil {
		return "", err
	}

	hasher := sha256.New()
	_, err = io.Copy(tmp, io.TeeReader(r, hasher))
	tmp.Close()
	if err != nil {
		return tmp.Name(), fmt.Errorf("download failed: %w", err)
	}

	actualSum := hex.EncodeToString(hasher.Sum(nil))
	if actualSum != expectedSum {
		return tmp.Name(), fmt.Errorf("checksum mismatch for downloaded archive:\n  expected: %s\n  got:      %s\nrefusing to install", expectedSum, actualSum)
	}

	return tmp.Name(), nil
}

// swapBinary replaces the current executable with the file at newPath.
// Safe for running processes: rename old → .old, rename new → target.
func swapBinary(newPath string) error {
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot find current binary: %w", err)
	}
	// Resolve symlinks to get the real path
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("resolve binary path: %w", err)
	}

	oldPath := execPath + ".old"

	// Remove any leftover .old file
	os.Remove(oldPath)

	// Step 1: Rename running binary out of the way
	if err := os.Rename(execPath, oldPath); err != nil {
		return fmt.Errorf("rename current binary: %w (%s)", err, reinstallHint())
	}

	// Step 2: Move new binary into place
	if err := os.Rename(newPath, execPath); err != nil {
		// Rollback: put the old binary back
		os.Rename(oldPath, execPath)
		return fmt.Errorf("install new binary: %w", err)
	}

	// Step 3: Try to remove old binary (works on Linux/macOS, may fail on Windows)
	os.Remove(oldPath)

	return nil
}

// cleanupOldBinary removes a stale .old binary from a previous update.
// Called at the start of every update to handle the Windows case where
// the .old file couldn't be deleted while the process was running.
func cleanupOldBinary() {
	execPath, err := os.Executable()
	if err != nil {
		return
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return
	}
	os.Remove(execPath + ".old")
}

// extractBinary extracts the named binary from the archive at archivePath
// into a temp file inside destDir (same volume as the final install path),
// returning the temp file path. Zip archives are what windows releases ship;
// everything else is tar.gz.
func extractBinary(archivePath, name, destDir string) (string, error) {
	if strings.HasSuffix(archivePath, ".zip") {
		return extractBinaryFromZip(archivePath, name, destDir)
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return extractBinaryFromTarGz(f, name, destDir)
}

// extractBinaryFromTarGz extracts a named file from a tar.gz stream
// and writes it to a temp file in destDir, returning the temp file path.
func extractBinaryFromTarGz(r io.Reader, name, destDir string) (string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", fmt.Errorf("gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return "", fmt.Errorf("%s not found in archive", name)
		}
		if err != nil {
			return "", fmt.Errorf("tar: %w", err)
		}

		if hdr.Name == name || strings.HasSuffix(hdr.Name, "/"+name) {
			return spoolToTemp(tr, destDir)
		}
	}
}

// extractBinaryFromZip extracts a named file from a zip archive and writes
// it to a temp file in destDir, returning the temp file path.
func extractBinaryFromZip(archivePath, name, destDir string) (string, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", fmt.Errorf("zip: %w", err)
	}
	defer zr.Close()

	for _, f := range zr.File {
		if f.Name == name || strings.HasSuffix(f.Name, "/"+name) {
			rc, err := f.Open()
			if err != nil {
				return "", fmt.Errorf("zip: %w", err)
			}
			tmp, err := spoolToTemp(rc, destDir)
			rc.Close()
			return tmp, err
		}
	}
	return "", fmt.Errorf("%s not found in archive", name)
}

// spoolToTemp writes r to a new temp file in dir and returns its path.
func spoolToTemp(r io.Reader, dir string) (string, error) {
	tmp, err := os.CreateTemp(dir, ".agend-update-*")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	tmp.Close()
	return tmp.Name(), nil
}
