# agend

CLI and MCP server for [agend.sh](https://agend.sh) -- remote dev environments exposed exclusively via MCP protocol. Your AI agent gets a full isolated Linux box (shell, files, networking, interactive apps) without knowing anything about the underlying infrastructure.

`agend mcp` acts as a stdio MCP server: it receives JSON-RPC tool calls from the agent, translates them to gRPC requests, and sends them through a WebSocket tunnel to `agentd` running inside a Firecracker microVM.

## Install

**Homebrew**

```sh
brew install agend-sh/tap/agend
```

**curl** (Linux/macOS)

```sh
curl -fsSL agend.sh/i | sh
```

The installer downloads the latest release from GitHub, verifies the SHA-256 checksum, and installs to `/usr/local/bin`.

**Windows** (PowerShell)

```powershell
irm agend.sh/i.ps1 | iex
```

Installs `agend.exe` to `%LOCALAPPDATA%\agend\bin` (checksum-verified) and adds it to your user `PATH`. Corporate proxies: Go binaries use the `HTTPS_PROXY`/`HTTP_PROXY` environment variables, not the Windows system proxy settings — set them if your network requires a proxy.

**From source**

```sh
git clone https://github.com/agend-sh/cli.git
cd cli
make install    # builds and copies to /usr/local/bin
```

Requires Go 1.24+.

## Quick start

```sh
agend signup --email you@example.com
agend login
agend env create
agend config claude    # or: cursor, windsurf, gemini, continue
```

The agent can now reach the environment. It sees MCP tools like `shell_exec`, `file_write`, `shell_provide_input` -- a full computer it can drive autonomously.

## Commands

### Auth

| Command | Description |
|---------|-------------|
| `agend signup --email <email>` | Create an account (interactive password prompt) |
| `agend login` | Authenticate via browser OAuth |
| `agend login --email <email>` | Authenticate with email/password |
| `agend login --token <token>` | Authenticate with a direct API token |
| `agend logout` | Clear local credentials |
| `agend status` | Show auth and environment status |
| `agend account list` | List saved accounts |
| `agend account switch <email>` | Switch the active account |
| `agend account remove <email>` | Remove a saved account |

Credentials (and any saved accounts) are stored in `~/.config/agend/credentials.json` (mode 0600).

### Environments

| Command | Description |
|---------|-------------|
| `agend env create` | Provision a new environment (boots a microVM from warm pool) |
| `agend env list` | List environments with state, tier, and endpoint |
| `agend env status [env-id]` | Show detailed environment info |
| `agend env wake [env-id]` | Wake a sleeping environment (restores from snapshot) |
| `agend env delete [env-id]` | Permanently delete an environment |

### Teams (shared environments)

A team owns a pool of shared environments. Members check one out at a time — a
**lease** grants exclusive access until released (or it times out). See ADR-020.

| Command | Description |
|---------|-------------|
| `agend team create <name>` | Create a team (you become the owner) |
| `agend team list` | List your teams |
| `agend team invite <team-id> <email>` | Invite a member by email |
| `agend team accept <team-id>` | Accept an invitation |
| `agend team members <team-id>` | List team members |
| `agend team envs <team-id>` | List the team's shared environments + lease status |
| `agend team env-create <team-id>` | Create a shared environment in the team pool |
| `agend env acquire <env-id>` | Check out a shared env (exclusive until released) |
| `agend env release <env-id>` | Release your lease |
| `agend env heartbeat <env-id>` | Extend your lease while connected |

`agend mcp` auto-acquires the active team env on start, heartbeats while connected,
and releases on exit — so an AI agent shares a team box seamlessly.

### Shell (direct gRPC)

| Command | Description |
|---------|-------------|
| `agend exec <cmd>` | Execute a command remotely |
| `agend input <text>` | Send input to a process waiting for input |
| `agend interrupt` | Send SIGINT (Ctrl+C) to the running command |
| `agend ping` | Check connectivity to agentd |

`exec` supports `--background` (returns task ID), `--interactive` (PTY mode for TUIs), `--timeout`, `--tail`, and `--head`.

### Files (direct gRPC)

| Command | Description |
|---------|-------------|
| `agend file-get <path>` | Download a file from the environment |
| `agend file-put <path>` | Upload a file to the environment |
| `agend file-move <src> <dst>` | Move or rename a file |

### Tasks (direct gRPC)

| Command | Description |
|---------|-------------|
| `agend task-output <id>` | Get output of a background task |
| `agend task-stop <id>` | Stop a background task |

### Custom domains

Expose a service running in your environment under your own domain.

| Command | Description |
|---------|-------------|
| `agend domain add <domain>` | Map a custom domain to an exposed port |
| `agend domain list` | List your mapped domains |
| `agend domain remove <domain>` | Remove a domain mapping |

### MCP server

| Command | Description |
|---------|-------------|
| `agend mcp` | Run as MCP server (stdio JSON-RPC transport) |
| `agend config <agent>` | Write MCP config for an AI agent |

### Other

| Command | Description |
|---------|-------------|
| `agend version` | Print CLI version |
| `agend update` | Self-update from GitHub Releases |

## MCP server mode

`agend mcp` implements the [Model Context Protocol](https://modelcontextprotocol.io) (protocol version 2024-11-05) over stdio. It reads JSON-RPC requests from stdin and writes responses to stdout.

The MCP server manages a **connection pool** -- each environment gets its own gRPC connection, resolved lazily on first tool call. If a connection drops (tunnel expiry, VM snapshot/restore, network flap), it automatically re-resolves the endpoint via the control plane API, wakes sleeping environments, and retries with backoff.

### Available tools

**Environment management** (API-only, no gRPC connection needed):

| Tool | Description |
|------|-------------|
| `list_environments` | List environments with IDs, state, tier |
| `env_create` | Create a new environment |
| `env_status` | Get environment state, tier, endpoint, timestamps |
| `env_wake` | Wake a sleeping environment |

**Shell** (routed to agentd via gRPC):

| Tool | Description |
|------|-------------|
| `shell_exec` | Execute commands (supports timeout, interactive, background, head/tail truncation) |
| `shell_provide_input` | Send text input to a waiting process (appends newline) |
| `shell_send_raw` | Send raw bytes to PTY (vim keystrokes, REPL input, no newline) |
| `shell_interrupt` | Send SIGINT to interrupt the running command |
| `shell_task_output` | Get output of a background task |
| `shell_task_stop` | Stop a background task |

**Files** (routed to agentd via gRPC):

| Tool | Description |
|------|-------------|
| `file_download` | Download file from environment to local machine (chunked, 1MB chunks) |
| `file_upload` | Upload local file to environment (chunked, 12KB chunks for Cloudflare compat) |
| `file_write` | Write text content directly to a remote file (atomic temp+rename) |
| `file_move` | Move or rename a file in the environment |

**Diagnostics**:

| Tool | Description |
|------|-------------|
| `env_stats` | Disk, memory, CPU, and top processes |

All tools that operate on an environment accept an `environment` parameter (env ID or alias). The agent calls `list_environments` first to discover available IDs.

### Supported agents

```sh
agend config claude        # Claude Desktop / Claude Code
agend config cursor        # Cursor
agend config windsurf      # Windsurf
agend config gemini        # Gemini
agend config continue      # Continue
```

`agend config` writes the agent-specific MCP configuration file, registering `agend mcp` as a stdio server.

## Connection architecture

```
AI Agent
  |  stdio (JSON-RPC / MCP)
  v
agend mcp
  |  gRPC over WebSocket (wss://)
  |  through Cloudflare Tunnel
  v
agentd (inside Firecracker microVM)
```

**Transport details:**

1. The agent communicates with `agend mcp` over stdio using MCP's JSON-RPC protocol.
2. `agend mcp` maintains a pool of gRPC connections (`internal/mcp/conn.go`), one per environment.
3. For Cloudflare tunnel endpoints (`*.trycloudflare.com` or `*.agend.sh`), gRPC runs over a WebSocket connection (`internal/grpc/proxy.go`). The `nhooyr.io/websocket` library handles Cloudflare's HTTP/1.1 upgrade.
4. For direct endpoints (dev/local), gRPC connects over plain TCP.
5. Auth uses a one-time secret (from env creation) exchanged for a session token on first RPC. The session token is persisted locally and reused across CLI invocations.

**Retry and reconnection** (`internal/mcp/conn.go`, `internal/mcp/errors.go`):

Errors are classified into four categories:
- **Fatal**: env not found, forbidden, context canceled -- returned immediately.
- **Auth**: unauthenticated -- re-resolve endpoint to get fresh credentials, retry once.
- **Transient**: 502/503, timeout, connection reset -- retry same endpoint with backoff (up to 3 attempts).
- **Stale endpoint**: tunnel dead, DNS not found, connection refused -- close connection, re-resolve via API (wakes sleeping envs), reconnect with backoff (up to 4 attempts, 3s intervals for DNS propagation).

## Project structure

```
cmd/agend/          Entry point (version injected via ldflags)
internal/
  cmd/              Cobra command definitions (root, env, exec, mcp, login, etc.)
  mcp/              MCP server: JSON-RPC handler, tool definitions, connection pool
  grpc/             gRPC client: dial logic, WebSocket tunnel dialer, auth interceptor
  api/              Control plane HTTP client (signup, login, env CRUD)
  auth/             Credential storage (~/.config/agend/credentials.json), browser OAuth
  setup/            Agent-specific MCP config writers (Claude, Cursor, Windsurf, Gemini, Continue)
proto/agentd/v1/    Generated protobuf/gRPC code for the agentd service
```

## Development

```sh
make build      # build to bin/agend
make test       # run tests
make install    # build + copy to /usr/local/bin
make proto      # regenerate protobuf (requires protoc + go plugins)
make release    # cross-compile linux/darwin x amd64/arm64
```

## CI/CD

- **CI** (`.github/workflows/ci.yml`): Runs on push/PR to main. Builds, tests, vets.
- **Release** (`.github/workflows/release.yml`): Triggered by `v*` tags. GoReleaser v2 builds 4 targets (linux/darwin x amd64/arm64), publishes to GitHub Releases, and updates the [Homebrew tap](https://github.com/agend-sh/homebrew-tap).

```sh
git tag v0.1.0
git push origin v0.1.0
```

## Self-update

```sh
agend update
```

Downloads the latest release tarball from GitHub, extracts the binary, and swaps it in place using a rename trick (old binary renamed to `.old`, new binary moved in). Safe while `agend mcp` is running -- the OS keeps the old binary in memory via the open fd. The new version takes effect on next launch.

## License

MIT
