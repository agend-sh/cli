# agend CLI

Go CLI + MCP stdio bridge (`agend mcp`) for agend.sh. Public repo — never commit
secrets, tokens, or internal hostnames beyond what's already public.

## Build / test

```sh
go build ./...          # also: GOOS=windows GOARCH=amd64 go build ./...
go test ./...
go vet ./...
```

CI runs the test suite on ubuntu-latest AND windows-latest, plus cross-compile
checks for darwin/arm64 and windows/arm64. Windows is a first-class target —
keep it green:

- Test home isolation must set BOTH `HOME` (unix) and `USERPROFILE` (windows);
  `os.UserHomeDir()` reads the latter on Windows. See `setupHome`/`isolateHome`.
- Don't assert unix mode bits (0600 etc.) without a `runtime.GOOS == "windows"`
  skip — they're no-ops there.
- The filesystem root is `/` on unix but a volume root (`C:\`) on Windows —
  use `filepath.VolumeName(...)` in root-path tests.
- No `golang.org/x/sys/unix`, `sh -c`, or raw-terminal code. OS-specific code
  goes in `_unix.go`/`_windows.go` files with build tags (see reexec_*.go).
- SIGTERM never fires on Windows; `agend mcp` shuts down via stdin EOF.

## Release

Tag `v*` → `.github/workflows/release.yml` → GoReleaser → GitHub release
(linux/darwin tar.gz, windows zip, checksums.txt + keyless cosign sig) →
Homebrew formula push to agend-sh/homebrew-tap.

- The brews step runs LAST and needs the `HOMEBREW_TAP_TOKEN` secret (a PAT
  with write on agend-sh/homebrew-tap). If it 401s, the GitHub release is
  still complete — only the formula is stale. Manual fix: regenerate
  Formula/agend.rb (bump version/urls/sha256 from checksums.txt) and push via
  `gh api -X PUT /repos/agend-sh/homebrew-tap/contents/Formula/agend.rb`.
- `goreleaser check` warns that `brews` is deprecated (→ homebrew_casks
  someday); it still works.
- Self-update (`internal/cmd/update.go`) mirrors the release layout: tar.gz +
  `agend` member everywhere, zip + `agend.exe` on Windows. If you change
  archive naming in .goreleaser.yaml, update `archiveName`/`binaryName` too.

## Install scripts (SECURITY-SENSITIVE)

`install.sh` (served at agend.sh/i) and `install.ps1` (agend.sh/i.ps1) are
fetched by agend-landing pinned to an immutable commit SHA of this repo — NOT
a branch — so push access to main can't change what curl|sh executes. After
changing either script, bump the pinned SHA in agend-landing in ALL of:
`deploy.sh`, `.github/workflows/deploy.yml`, `src/routes/i/+server.ts`,
`src/routes/i.ps1/+server.ts`, then redeploy the landing.

## Layout notes

- `internal/mcp` — MCP stdio server; stdout is protocol-only, all logging to
  stderr. Local file access confined to cwd / AGEND_LOCAL_ROOT (paths.go).
- `internal/recovery` — single source of truth for connection-error
  classification; includes Windows socket-error spellings ("forcibly closed",
  "actively refused") alongside the unix ones.
- `proto/` is generated and duplicated in the daemon, cli, and node-agent
  repos — change proto definitions in all three.
- Git identity for this repo: raul@agend.sh (per-repo config, never global).
