package cmd

import (
	"archive/tar"
	"compress/gzip"
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
			req, err := http.NewRequest("GET", releasesURL, nil)
			if err != nil {
				return err
			}
			req.Header.Set("Accept", "application/vnd.github+json")

			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("failed to check for updates: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != 200 {
				return fmt.Errorf("GitHub API returned %d", resp.StatusCode)
			}

			var release githubRelease
			if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
				return fmt.Errorf("failed to parse release: %w", err)
			}

			latest := strings.TrimPrefix(release.TagName, "v")
			current := strings.TrimPrefix(currentVersion, "v")

			if latest == current && !force {
				fmt.Printf("already up to date (%s)\n", currentVersion)
				return nil
			}

			fmt.Printf("found %s (current: %s)\n", release.TagName, currentVersion)

			// Download tar.gz archive
			archiveName := fmt.Sprintf("agend-%s-%s-%s.tar.gz", latest, runtime.GOOS, runtime.GOARCH)
			url := fmt.Sprintf("%s/%s/%s", downloadURL, release.TagName, archiveName)

			fmt.Printf("Downloading %s... ", archiveName)
			dlResp, err := client.Get(url)
			if err != nil {
				return fmt.Errorf("download failed: %w", err)
			}
			defer dlResp.Body.Close()

			if dlResp.StatusCode != 200 {
				return fmt.Errorf("download failed: HTTP %d", dlResp.StatusCode)
			}

			// Extract agend binary from tar.gz
			tmpBinary, err := extractBinaryFromTarGz(dlResp.Body, "agend")
			if err != nil {
				return fmt.Errorf("extract failed: %w", err)
			}
			defer os.Remove(tmpBinary)

			if err := os.Chmod(tmpBinary, 0755); err != nil {
				return err
			}

			// Replace current binary using rename-swap trick.
			// This is safe even if another agend process (e.g. `agend mcp`) is running:
			// - Linux/macOS: the OS keeps the old binary in memory via the open fd;
			//   renaming the file on disk doesn't affect the running process.
			// - Windows: you can rename a running .exe but can't delete it.
			//   We rename current → .old, put new in place, and clean up .old on next run.
			if err := swapBinary(tmpBinary); err != nil {
				return err
			}

			fmt.Println("done!")
			fmt.Printf("Updated to %s\n", release.TagName)
			fmt.Println("Running MCP servers will use the new version on next restart.")
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "re-download even if already up to date")

	return cmd
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
		return fmt.Errorf("rename current binary: %w (try: curl -fsSL agend.sh/i | sh)", err)
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

// extractBinaryFromTarGz extracts a named file from a tar.gz stream
// and writes it to a temp file, returning the temp file path.
func extractBinaryFromTarGz(r io.Reader, name string) (string, error) {
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
			tmp, err := os.CreateTemp("", "agend-update-*")
			if err != nil {
				return "", err
			}

			if _, err := io.Copy(tmp, tr); err != nil {
				tmp.Close()
				os.Remove(tmp.Name())
				return "", err
			}
			tmp.Close()
			return tmp.Name(), nil
		}
	}
}
