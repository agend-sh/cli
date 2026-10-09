# agend-sh MCPB bundle

This bundle runs the Agend MCP bridge locally through the published Docker
image. AI agents get persistent hosted Linux environments with interactive
terminals, file tools, background tasks, and public HTTPS previews.

## Requirements

- Docker with Linux containers enabled and its daemon running.
- The `docker` command available to your MCP client.
- Node.js 20 or newer, or a compatible client-provided Node.js runtime.
- An Agend account and available environment quota.

The image supports Linux amd64 and arm64. Docker runs it on Linux, macOS, and
Windows with Linux containers enabled. The small Node.js launcher uses only
the standard library. The bundle contains no account credentials.

## Authenticate

1. [Create a free seven-day account](https://agend.sh/account?signup=1), or use
   your existing Agend account.
2. Install the [Agend CLI](https://github.com/agend-sh/cli#quick-start) and run
   `agend login`.
3. Follow the [container authentication guide](https://github.com/agend-sh/cli/blob/main/docs/mcp-registry.md#connect-using-the-container)
   to load your active account token without printing it.
4. Supply that token in the bundle's **Agend account token** configuration
   field. The field is marked required and sensitive.
5. Ask the agent to call `list_environments`, then use an environment ID or
   name in subsequent calls. If needed, use `env_create` within your quota.

Refresh the configured token after reauthentication when it expires. The
bundle does not inherit your native CLI's selected environment.

## Terminals and files

Use `shell_exec` with `interactive=true` to launch a REPL or terminal app.
Continue through `shell_send_raw` and keep the MCP connection open during
interaction. An `input_wait` result is a per-response observation of a guest
terminal input-wait event, not proof of application readiness.

Remote files persist in the Agend environment. Local transfers use the
container's `/workspace`; host folders are not mounted automatically. Use
`file_write` for remote text files, or follow the
[workspace mount guide](https://github.com/agend-sh/cli/blob/main/docs/mcp-registry.md#local-file-transfers)
to configure direct Docker usage for transfers to or from your computer.

## Build and publish

From a Git checkout of this repository, with Python 3 and Go installed:

```sh
python3 mcpb/build.py
```

The builder writes `dist/mcpb/agend-sh-1.2.14.mcpb`, its checksum, the generated
manifest, the full server card, and a Smithery release payload. It exports all
23 tool definitions from the exact source commit in `mcpb/server/image.json`.
It never starts the bridge or contacts an Agend environment.

The archive has an explicit file list, a pinned image digest, and fixed ZIP
timestamps. Rebuild after updating the image version, source commit, and
digest together. This source currently packages Agend v1.2.14.

Smithery accepts local stdio releases as an MCPB upload. Use
`publish-payload.json` with the bundle in the
[publish API](https://smithery.ai/docs/api-reference/servers/publish-a-server)
to preserve the full tool input schemas. MCPB's manifest lists tool names
and descriptions; the separate Smithery server card includes those schemas.

[Smithery's publishing guide](https://smithery.ai/docs/build/publish) describes
the publication and verification process. Publication requires Smithery
account access; building a bundle alone does not publish a listing.

Report security issues privately through the
[security policy](https://github.com/agend-sh/cli/blob/main/SECURITY.md).
