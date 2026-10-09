# README verification

## Release and scope

Checked on **2026-10-09** against the published **[v1.2.13 release](https://github.com/agend-sh/cli/releases/tag/v1.2.13)**, source commit `b364a1aa247208632e399f2f1f12bbede8ccae8f`.

Live commands used the published binaries, not a modified CLI. Remote checks ran in disposable environments created for this audit with the account's default profile: 2 vCPU, 4096 MiB RAM, and a 20 GB data disk. Production environments belonging to other work were not selected. Local credentials and client configuration were isolated from the normal user setup.

This report distinguishes running a feature from inspecting its implementation. It is not a claim that every account, platform, or client application was exercised.

## Checks performed

| Area | Method | Result |
| --- | --- | --- |
| Linux installation | Ran the unmodified script served at `https://agend.sh/i` in a filesystem namespace with a disposable `/usr/local/bin`. | Signature and checksum verification passed; installed binary reported `1.2.13 (linux/amd64)`. |
| Windows installation | Ran `https://agend.sh/i.ps1` in native Windows PowerShell 5.1 with temporary install/config directories. | Signature and checksum verification passed; installed binary reported `1.2.13 (windows/amd64)`. The original user PATH was restored afterward. |
| Self-update | Ran `agend update --force` on disposable Linux and native Windows binaries. | Download, signature verification, replacement, and subsequent `version` calls passed. |
| Homebrew | Read the live tap formula and release metadata. | Formula targeted v1.2.13 and the published platform archives. Homebrew installation was not run. |
| Source build | Ran `make build` with Go 1.26.7. | Passed. |
| Client config | Ran explicit configuration for all 17 clients twice in an isolated home; exercised dry run and detection. | All 17 config writers completed successfully on Linux. Eight also completed on native Windows. This verifies config generation, not each client's UI or model integration. |
| MCP startup | Started `agend mcp` and performed JSON-RPC `initialize`, initialized notification, and `tools/list` on Linux and native Windows. | Valid protocol responses; 23 tools returned on both platforms. |
| Environment creation | Created one environment through the CLI and another through MCP; checked metadata and ran a command. | Passed. Both were subsequently deleted through the CLI. |
| Environment selection | Selected the disposable environment by ID and ran `ping` and `exec`. | Passed. Selecting by name through CLI `env use` failed with 404; the README now specifies IDs for CLI commands. |
| Profiles and metadata | Listed profiles; edited and cleared the disposable environment's description through CLI; updated metadata through MCP. | Passed. |
| Shell | Exercised ordinary execution, timeout, head/tail truncation, background start, output, and stop. | Passed. The timeout diagnostic has a wording issue described below. |
| Interactive Python | Started Python with `interactive: true`; observed `input_wait: true`; sent commands, resized, and exited. | Passed. Tool results were MCP text content, as shown in the README. |
| MCP reconnect | Closed MCP after a Python response, started a new MCP process, and sent another command to the same session. | The Python session survived and returned the new output. This did not test cancellation of an in-flight call, environment sleep, crash, or reset. |
| Simple prompt | Started a shell `read` prompt with a PTY and answered with `shell_provide_input`. | Passed. An ordinary command without a PTY did not leave an input session open. |
| Vim | Opened Vim through MCP, entered text, saved, and quit; read the resulting file. | Passed; saved text matched. |
| Watch | Ran `agend watch --typing-delay 20ms` in a real local PTY while MCP drove Python. Quit the observer with `q`, then sent another Python command. | Mirror received the session output; stopping watch left the remote session running. |
| Connect | Opened a real local PTY, ran a remote command, and exited the shell; repeated five times after the initial checks. | Commands ran successfully, but clean exit was intermittent. Two of the five repeat trials returned the error below; three exited with code 0. |
| Files | Exercised CLI put/get/move and MCP write/upload/download/move; compared contents. | Passed. An MCP local path outside the allowed root was correctly rejected. |
| Browser preview | Started a Python HTTP server, exposed port 8080, listed the URL, and fetched a known file over public HTTPS. | Content matched. Newly created tunnel DNS required propagation time. This machine's normal resolver initially failed; DNS over HTTPS plus `curl --resolve` verified the public endpoint. Exposure and server task were removed. |
| Accounts | Exercised local token storage, account listing, switching, and removal using a synthetic second token in the isolated store. | Passed. The synthetic token was not used against the service; this does not verify live email/password or OAuth login. |
| Teams and domains | Listed existing teams, members, team profiles, shared environments, and registered domains. | Read-only commands passed. Team creation, invitations, lease mutation, and domain registration were not exercised. |
| Tests and vet | Ran `go test ./...` and `go vet ./...` from the source checkout. | Passed. Existing tests include wake/background recovery, cold-reset argument validation, credential handling, and OAuth exchange validation. These are not live lifecycle or browser-login checks. |
| README consistency | Matched the tool table against live `tools/list`; parsed JSON examples and rendered the Markdown in a browser. | All 23 tools covered; JSON parsed; logo and badges loaded. Preview had no horizontal overflow at desktop and 390 px widths. This was a local GFM rendering, not a published GitHub page. |

**21 of the 23 MCP tools were exercised successfully against live disposable environments.** `env_wake` and `env_cold_reset` were not live-tested: the disposable profile never sleeps, and a healthy live environment was not forcibly transitioned to manufacture a recovery scenario.

## Current issues

### Intermittent error when `connect` exits

After successfully running a command and sending `exit`, some runs returned CLI exit code 1 with:

```text
connect: rpc error: code = FailedPrecondition desc = no active session waiting for input
```

This reproduced in two of five repeat trials. Other trials exited normally. The README documents the behavior; no backend fix or production deployment was made as part of this documentation audit. This remains a release issue to investigate.

### Unix installer prints an obsolete setup command

The successfully verified Unix installer ends with:

```text
agend setup claude
```

The released CLI has no `setup` command, and `agend config claude` also fails. The working client names are `claude-code` and `claude-desktop`. The README quick start and troubleshooting now give those commands. The served installer was not changed by this audit.

### Misleading timeout diagnostic

A noninteractive `sleep` with a short timeout returned `status: timeout` and this diagnostic:

```text
[timed out with no output — interactive host-shim support is not enabled yet]
```

Interactive Python and Vim worked in the same environment. The status correctly indicated a timeout; the explanatory text did not describe the environment's actual interactive support.

## Limits of this verification

- Fresh production signup and email/password login were not performed. No password was available for the existing account, and no new signup email address was supplied. Successful live calls used the existing account token in an isolated local store.
- Browser OAuth was not completed. The test browser had no authenticated GitHub session. Source and existing OAuth tests were inspected; that does not establish the entire live browser flow.
- Native macOS, native arm64 execution, and an actual Homebrew install were not available on this workstation.
- Config generation for 17 clients does not establish that every client application successfully launched the server. Direct MCP startup was verified on Linux and Windows.
- Team invitation/acceptance and ownership changes, shared-environment lease mutation, and custom domain registration/removal were not performed. A temporary public tunnel was verified without changing existing domain registrations.
- Automatic idle sleep, wake from a real snapshot, and cold reset were not exercised on production. No environment backup, forced sleep, infrastructure change, or service restart was performed.

Raw local transcripts contain account and environment details and are kept outside the public repository. This report contains the relevant outcomes without credentials or production environment identifiers.
