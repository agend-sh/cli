#!/bin/sh
set -eu

# AGEND_API_TOKEN is a container input. The native CLI reads its credential
# store, so use its existing login command to save the supplied account token.
# Keep login output off the MCP protocol stream. A mounted credential store
# can also be used when AGEND_API_TOKEN is omitted.
if [ "${1:-}" = "mcp" ] && [ -n "${AGEND_API_TOKEN:-}" ]; then
    agend login --token "$AGEND_API_TOKEN" >&2
fi

exec agend "$@"
