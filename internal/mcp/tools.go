package mcp

// envProp is the required environment parameter added to every tool.
var envProp = map[string]any{"type": "string", "description": "Environment ID (from list_environments)"}

func toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "list_environments",
			"description": "List available environments with their IDs, state, and tier. Call this first to get the environment ID needed by all other tools.\n\nEnvironment details:\n- Full Ubuntu 24.04 with Python 3.12, Node 22, Go 1.24, Rust, Java 21, Ruby, PHP, build-essential, and common dev tools.\n- Filesystem uses overlayfs: the base image is read-only, all writes go to a separate data drive. 'df /' may show the base image size (~3.5GB) but your writable space is on the overlay (check 'df /mnt/upper' for actual free space).\n- Pre-installed tools: git, gh, ripgrep, fd-find, jq, curl, cmake, make, sqlite3, postgresql-client, mysql-client.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"name": "shell_exec",
			"description": `Execute a command in a remote environment.

Returns one of three statuses:
- "completed": Command finished. Check stdout, stderr, and exit_code.
- "awaiting_input": Command is waiting for input (password, REPL prompt, TUI app).
- "timeout": Command exceeded timeout_ms.

INTERACTIVE MODE (set interactive=true):
Use for REPLs (python3, node, jshell, psql, sqlite3, irb, ghci, etc.) and
TUI apps (vim, nano, htop, less, top, etc.) that expect ongoing interaction.

When interactive=true:
- The process stays alive across calls — it is NOT killed on timeout.
- After the initial shell_exec returns "awaiting_input", you MUST use
  shell_send_raw for ALL subsequent interaction with the program.
- Do NOT call shell_exec again while an interactive session is active.
- Do NOT use shell_provide_input for interactive apps (it's for passwords only).

Interactive workflow:
1. shell_exec(command="python3", interactive=true) → "awaiting_input"
2. shell_send_raw(input="print('hello')\n") → shows output + "awaiting_input"
3. shell_send_raw(input="exit()\n") → "completed"

To close an interactive session:
- Send the app's quit command via shell_send_raw (e.g. "exit()\n", "/exit\n", ":q!\n")
- Or call shell_interrupt to force kill it`,
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environment":       envProp,
					"command":           map[string]any{"type": "string", "description": "The command to execute"},
					"timeout_ms":        map[string]any{"type": "integer", "description": "Timeout in milliseconds (default: 30000)"},
					"interactive":       map[string]any{"type": "boolean", "description": "Set to true for REPLs and TUI apps. Mutually exclusive with run_in_background."},
					"run_in_background": map[string]any{"type": "boolean", "description": "Return task_id immediately. Mutually exclusive with interactive."},
					"tail_lines":        map[string]any{"type": "integer", "description": "Return only last N lines"},
					"head_lines":        map[string]any{"type": "integer", "description": "Return only first N lines"},
				},
				"required": []string{"environment", "command"},
			},
		},
		{
			"name": "shell_provide_input",
			"description": `Send text input to a process waiting for a simple prompt (password, confirmation, yes/no).
Appends a newline automatically.

DO NOT use this for interactive apps (REPLs, TUI apps) — use shell_send_raw instead.
This tool is only for simple prompts like sudo password, SSH confirmations, or [Y/n] prompts.`,
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environment": envProp,
					"input":       map[string]any{"type": "string", "description": "Text input (newline appended automatically)"},
				},
				"required": []string{"environment", "input"},
			},
		},
		{
			"name": "shell_send_raw",
			"description": `Send raw bytes to an interactive program (REPL, TUI app). No newline appended — include \n explicitly if needed.

This is THE tool for interacting with any program launched with interactive=true.
After shell_exec with interactive=true returns "awaiting_input", use this tool for ALL
subsequent interaction.

Common patterns:
- REPL input: shell_send_raw(input="print('hello')\n")
- Vim save/quit: shell_send_raw(input=":wq\n")
- Exit REPL: shell_send_raw(input="exit()\n")
- Ctrl+D (EOF): shell_send_raw(input="\x04")
- Escape key: shell_send_raw(input="\x1b")
- Arrow keys: shell_send_raw(input="\x1b[A") (up), \x1b[B (down)

Escape sequences: \n (newline), \t (tab), \x1b (escape), \x04 (Ctrl+D), \x03 (Ctrl+C)`,
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environment": envProp,
					"input":       map[string]any{"type": "string", "description": "Raw bytes sent directly to PTY (include \\n for newline)"},
				},
				"required": []string{"environment", "input"},
			},
		},
		{
			"name":        "shell_interrupt",
			"description": "Send SIGINT (Ctrl+C) to interrupt the running command or close an interactive session.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environment": envProp,
				},
				"required": []string{"environment"},
			},
		},
		{
			"name":        "shell_task_output",
			"description": "Get the output of a background task started with run_in_background.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environment": envProp,
					"task_id":     map[string]any{"type": "string", "description": "The task ID"},
				},
				"required": []string{"environment", "task_id"},
			},
		},
		{
			"name":        "shell_task_stop",
			"description": "Stop a running background task.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environment": envProp,
					"task_id":     map[string]any{"type": "string", "description": "The task ID"},
				},
				"required": []string{"environment", "task_id"},
			},
		},
		{
			"name":        "file_download",
			"description": "Download a file from the remote environment to the local machine. Returns metadata (size, checksum) only — no file content in the response. Use shell_exec('cat file') to read text content.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environment": envProp,
					"remote_path": map[string]any{"type": "string", "description": "Absolute path on the remote environment"},
					"local_path":  map[string]any{"type": "string", "description": "Local path to save the file. Must be inside the MCP server's working directory (or AGEND_LOCAL_ROOT if set); paths outside it are rejected."},
				},
				"required": []string{"environment", "remote_path", "local_path"},
			},
		},
		{
			"name":        "file_upload",
			"description": "Upload a file from the local machine to the remote environment. Transfers are atomic (temp file + rename). Uses small chunks for security — uploads over 1MB may be slow. For large files, prefer shell_exec with curl/wget to download directly inside the environment.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environment": envProp,
					"local_path":  map[string]any{"type": "string", "description": "Local file path to upload. Must be inside the MCP server's working directory (or AGEND_LOCAL_ROOT if set); paths outside it are rejected."},
					"remote_path": map[string]any{"type": "string", "description": "Absolute path on the remote environment"},
					"mode":        map[string]any{"type": "string", "description": "File permissions (e.g. '0644')"},
					"create_dirs": map[string]any{"type": "boolean", "description": "Create parent directories"},
				},
				"required": []string{"environment", "local_path", "remote_path"},
			},
		},
		{
			"name":        "file_write",
			"description": "Write text content directly to a file in the remote environment. For small files (configs, scripts, code). Uses atomic writes (temp + rename). For large/binary files, use file_upload instead.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environment": envProp,
					"path":        map[string]any{"type": "string", "description": "Absolute path on the remote environment"},
					"content":     map[string]any{"type": "string", "description": "File content (text)"},
					"mode":        map[string]any{"type": "string", "description": "File permissions (e.g. '0644')"},
					"create_dirs": map[string]any{"type": "boolean", "description": "Create parent directories"},
				},
				"required": []string{"environment", "path", "content"},
			},
		},
		{
			"name":        "file_move",
			"description": "Move or rename a file in a remote environment.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environment": envProp,
					"source":      map[string]any{"type": "string", "description": "Source path"},
					"destination": map[string]any{"type": "string", "description": "Destination path"},
				},
				"required": []string{"environment", "source", "destination"},
			},
		},
		{
			"name":        "env_stats",
			"description": "Get resource usage stats for an environment: disk space, memory, CPU, running processes. Quick health check without running shell commands.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environment": envProp,
				},
				"required": []string{"environment"},
			},
		},
		{
			"name":        "port_expose",
			"description": "Expose a port to the internet via Cloudflare Tunnel. Returns a public URL that routes to the specified port in the environment. For custom domains outside agend.sh (e.g., 'app.example.com'), also pass cf_token, cf_account_id, and cf_zone_id for the zone that owns the domain.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environment":   envProp,
					"port":          map[string]any{"type": "integer", "description": "Port number to expose (1-65535)"},
					"domain":        map[string]any{"type": "string", "description": "Custom domain to expose on (e.g., 'app.example.com'). If omitted, returns a temporary tunnel URL."},
					"cf_token":      map[string]any{"type": "string", "description": "Optional: Cloudflare API token with DNS edit + Tunnel rights on the domain's zone. Only needed for domains outside the default agend.sh zone."},
					"cf_account_id": map[string]any{"type": "string", "description": "Optional: Cloudflare account ID that owns the named tunnel. Defaults to the environment's account."},
					"cf_zone_id":    map[string]any{"type": "string", "description": "Optional: Cloudflare zone ID for the domain's DNS. Required when 'domain' is outside the default agend.sh zone."},
				},
				"required": []string{"environment", "port"},
			},
		},
		{
			"name":        "port_unexpose",
			"description": "Stop exposing a port. Without 'domain', removes every domain and/or quick tunnel bound to the port. With 'domain', unbinds only that domain and leaves sibling domains on the port intact.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environment": envProp,
					"port":        map[string]any{"type": "integer", "description": "Port number to unexpose"},
					"domain":      map[string]any{"type": "string", "description": "Optional: unbind only this custom domain from the port (e.g., 'app.example.com'). If omitted, all domains and the quick tunnel on the port are removed."},
				},
				"required": []string{"environment", "port"},
			},
		},
		{
			"name":        "port_list",
			"description": "List all currently exposed ports and their public URLs.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environment": envProp,
				},
				"required": []string{"environment"},
			},
		},
		// ── Environment management (API only, no gRPC needed) ──
		{
			"name":        "env_create",
			"description": "Create a new environment. Returns the environment ID and endpoint. The environment boots a fresh microVM from the warm pool.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			"name":        "env_status",
			"description": "Get detailed status of an environment including state, tier, endpoint, and timestamps.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environment": envProp,
				},
				"required": []string{"environment"},
			},
		},
		{
			"name":        "env_wake",
			"description": "Wake a sleeping environment. Restores the VM from snapshot with a new endpoint.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"environment": envProp,
				},
				"required": []string{"environment"},
			},
		},
		{
			"name":        "reload_config",
			"description": "Reload CLI credentials from disk. Call this after signing up, logging in, or creating environments in another terminal. Resets all connections.",
			"inputSchema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}
