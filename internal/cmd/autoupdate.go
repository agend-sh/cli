package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Auto-update keeps a public CLI current without the user thinking about it.
// At most once per autoUpdateInterval it checks GitHub for a newer release; if
// one exists it installs it (checksum-verified) and re-execs the running
// process into the new binary. The re-exec preserves the PID and inherited
// stdio, so an `agend mcp` server stays connected to its client across the
// upgrade — no dropped session, no manual restart.
//
// It always fails open: any error (offline, GitHub down, read-only install,
// re-exec unsupported) leaves the current process running unchanged.
const (
	autoUpdateInterval = 24 * time.Hour
	updateStateFile    = "update-check.json"
)

type updateState struct {
	LastCheckUnix int64  `json:"last_check_unix"`
	LatestVersion string `json:"latest_version"`
}

// MaybeAutoUpdate is the entrypoint, called once from main before the command
// runs. currentVersion is the build version; args is os.Args.
func MaybeAutoUpdate(currentVersion string, args []string) {
	// Auto-update must never break the CLI — swallow anything unexpected.
	defer func() { _ = recover() }()

	if !autoUpdateEnabled(currentVersion, args) {
		return
	}

	statePath := updateStatePath()
	st := loadUpdateState(statePath)

	// Rate-limit the network check; between checks, reuse the cached result.
	latest := st.LatestVersion
	if time.Since(time.Unix(st.LastCheckUnix, 0)) > autoUpdateInterval {
		tag, err := fetchLatestTag(&http.Client{Timeout: 3 * time.Second})
		if err != nil {
			return // transient — try again after the interval
		}
		latest = tag
		saveUpdateState(statePath, updateState{LastCheckUnix: time.Now().Unix(), LatestVersion: tag})
	}

	if !isNewer(latest, currentVersion) {
		return
	}

	// Resolve where the new binary will live BEFORE installing — swapBinary
	// unlinks the old inode, after which os.Executable() points at a deleted
	// path and can't be re-exec'd. This is the same path swapBinary installs to.
	target, err := os.Executable()
	if err == nil {
		if resolved, rerr := filepath.EvalSymlinks(target); rerr == nil {
			target = resolved
		}
	}

	// A newer release exists. Install it, then re-exec transparently.
	fmt.Fprintf(os.Stderr, "agend: updating %s → %s ...\n", displayVersion(currentVersion), latest)
	if err := installVersion(&http.Client{Timeout: 30 * time.Second}, latest); err != nil {
		fmt.Fprintf(os.Stderr, "agend: auto-update skipped (%v) — run 'agend update' to update manually.\n", err)
		return
	}
	if target == "" || reExec(target, args) != nil {
		// Re-exec unsupported (Windows) or unresolvable: the new binary is on
		// disk and takes effect on the next launch.
		fmt.Fprintln(os.Stderr, "agend: updated — the new version takes effect on next launch.")
	}
}

// autoUpdateEnabled gates auto-update off for dev builds, explicit opt-out, the
// post-re-exec child, and commands where a mid-run swap would be wrong.
func autoUpdateEnabled(version string, args []string) bool {
	if os.Getenv("AGEND_SELF_UPDATED") == "1" { // loop guard after a re-exec
		return false
	}
	if v := os.Getenv("AGEND_NO_AUTOUPDATE"); v != "" && v != "0" {
		return false
	}
	// Dev / unstamped builds have no real release to compare against.
	if version == "" || strings.Contains(version, "dev") {
		return false
	}
	switch firstSubcommand(args) {
	case "update", "version", "help", "completion", "__complete", "__completeNoDesc":
		return false
	}
	for _, a := range args[1:] {
		if a == "--version" || a == "-v" || a == "--help" || a == "-h" {
			return false
		}
	}
	return true
}

// firstSubcommand returns the first non-flag argument (the cobra subcommand).
func firstSubcommand(args []string) string {
	for _, a := range args[1:] {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// isNewer reports whether tag (e.g. "v1.2.0") is a strictly higher semver than
// current. Unparseable versions return false — auto-update only ever moves
// forward on a confidently-greater release.
func isNewer(tag, current string) bool {
	l, c := parseSemver(tag), parseSemver(current)
	if l == nil || c == nil {
		return false
	}
	for i := 0; i < 3; i++ {
		if l[i] != c[i] {
			return l[i] > c[i]
		}
	}
	return false
}

func parseSemver(v string) []int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 { // drop pre-release / build metadata
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return nil
	}
	out := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out[i] = n
	}
	return out
}

func displayVersion(v string) string {
	if v == "" {
		return "(unknown)"
	}
	if !strings.HasPrefix(v, "v") {
		return "v" + v
	}
	return v
}

func updateStatePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "agend", updateStateFile)
}

func loadUpdateState(path string) updateState {
	var st updateState
	if path == "" {
		return st
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return st
	}
	_ = json.Unmarshal(data, &st)
	return st
}

func saveUpdateState(path string, st updateState) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return
	}
	data, err := json.Marshal(st)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0600)
}
