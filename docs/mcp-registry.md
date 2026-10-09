# agend-sh in the official MCP Registry

The registry name is **`io.github.agend-sh/agend-sh`** and the display name is
**agend-sh**. The registry publishes installation metadata for the local stdio
MCP server. Agend hosts the Linux environments that the tools operate on.

The container package supports **linux/amd64 and linux/arm64**. Docker runs it
on Linux, macOS, or Windows with Linux containers enabled. An Agend account
and available environment quota are required.

## Connect using the container

1. Install the [Agend CLI](../README.md#quick-start) and run `agend login`.
2. Load your active account's token into `AGEND_API_TOKEN` as shown below.
3. Configure your MCP client to start the container.

The token is stored by the native CLI in `~/.config/agend/credentials.json`.
Read only your active account's token. These commands put it in the environment
without printing it.

**Linux / macOS** (requires `jq`):

```sh
export AGEND_API_TOKEN="$(jq -er '.accounts[.active].token' "$HOME/.config/agend/credentials.json")"
```

**Windows / PowerShell**:

```powershell
$agendCredentials = Get-Content "$env:USERPROFILE/.config/agend/credentials.json" -Raw | ConvertFrom-Json
$agendAccount = $agendCredentials.accounts.PSObject.Properties[$agendCredentials.active].Value
$env:AGEND_API_TOKEN = $agendAccount.token
```

Start the MCP server with:

```sh
docker run --rm -i --init --env AGEND_API_TOKEN ghcr.io/agend-sh/agend-mcp:1.2.14
```

Use `-i` without `-t`: the MCP transport requires plain standard input/output.
The container uses the CLI's existing token login and sends its login message
to stderr. It then runs `agend mcp`.

For clients using an `mcpServers` configuration:

```json
{
  "mcpServers": {
    "agend-sh": {
      "command": "docker",
      "args": [
        "run", "--rm", "-i", "--init", "--env", "AGEND_API_TOKEN",
        "ghcr.io/agend-sh/agend-mcp:1.2.14"
      ]
    }
  }
}
```

The MCP client process must inherit `AGEND_API_TOKEN`. A client launched from
a desktop icon may need its own secret/environment configuration instead.
Registry consumers can prompt for this secret using `server.json`.

Ask the agent to use `list_environments`, then name the environment in tool
calls. The container does not inherit the native CLI's selected environment.
You can create an environment with the native CLI or with `env_create` if
your account has quota. Reauthenticate and refresh the supplied token when
it expires.

### Local file transfers

The container's local transfer root is `/workspace`. Bind a directory there
to upload or download files from your computer, for example:

```sh
docker run --rm -i --init --env AGEND_API_TOKEN \
  --mount "type=bind,source=$(pwd),target=/workspace" \
  ghcr.io/agend-sh/agend-mcp:1.2.14
```

Local tool paths must be inside `/workspace`. The container runs as UID/GID
10001; a bind-mounted directory must allow that user to access the files.
Remote projects and interactive processes live in the Agend environment;
the local container does not store the remote workspace.

### Authenticate directly in Docker

You can use an Agend credential volume instead of passing a token:

```sh
docker run --rm -it \
  --mount type=volume,source=agend-mcp-config,target=/home/agend/.config/agend \
  ghcr.io/agend-sh/agend-mcp:1.2.14 login --email you@example.com

docker run --rm -i --init \
  --mount type=volume,source=agend-mcp-config,target=/home/agend/.config/agend \
  ghcr.io/agend-sh/agend-mcp:1.2.14
```

The first command prompts for your password and saves credentials in the
volume. The registry's default configuration uses the token route above.

## Maintainer publication

`server.json` describes the package and its required secret input. The root
`Dockerfile` builds the native CLI from this public repository, using pinned
Go and Debian images. `.dockerignore` restricts the build context to source
and packaging files. The container disables CLI auto-update so its binary
stays at the packaged release version.

After source CI succeeds, a stable `v*` tag runs `publish-mcp.yml` alongside
the normal native-binary release workflow. The registry workflow:

1. Builds amd64 and arm64 images, with provenance and an SBOM.
2. Pushes `ghcr.io/agend-sh/agend-mcp:<version>`.
3. Requires anonymous image access.
4. Rewrites the descriptor to use the exact image digest and release version.
5. Uploads that descriptor as a workflow artifact.
6. Authenticates with GitHub OIDC and publishes the descriptor to the registry.

**First publication:** GitHub creates new container packages as private. An
organization owner must change only the `agend-mcp` package to **Public** in
[package settings](https://github.com/orgs/agend-sh/packages/container/agend-mcp/settings).
Then rerun the failed registry job. Later releases use the same public package.

To publish an existing tagged version again, dispatch the workflow:

```sh
gh workflow run publish-mcp.yml -R agend-sh/cli -f version=1.2.14
```

The workflow checks out that exact release tag. It uses the repository's
temporary `GITHUB_TOKEN` to push the image and GitHub OIDC for the MCP
Registry. No Agend account credential is supplied to the publishing workflow.

References:

- [Official registry package formats](https://modelcontextprotocol.io/registry/package-types)
- [GitHub Actions publishing](https://modelcontextprotocol.io/registry/github-actions)
- [GitHub Container Registry visibility](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry)
