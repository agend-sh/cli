#!/bin/sh
set -e

REPO="agend-sh/cli"
INSTALL_DIR="/usr/local/bin"
BINARY="agend"

# Detect OS and arch
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m)

case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  aarch64|arm64) ARCH="arm64" ;;
  *) echo "Unsupported architecture: $ARCH" && exit 1 ;;
esac

case "$OS" in
  linux) OS="linux" ;;
  darwin) OS="darwin" ;;
  *) echo "Unsupported OS: $OS" && exit 1 ;;
esac

# Get latest release
LATEST=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | sed -E 's/.*"([^"]+)".*/\1/')

if [ -z "$LATEST" ]; then
  echo "Failed to fetch latest release."
  exit 1
fi

VERSION="${LATEST#v}"
ARCHIVE="${BINARY}-${VERSION}-${OS}-${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${LATEST}/${ARCHIVE}"
CHECKSUMS_URL="https://github.com/${REPO}/releases/download/${LATEST}/checksums.txt"

echo "Installing agend ${LATEST} (${OS}/${ARCH})..."

TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"' EXIT

# Download archive and checksums
curl -fsSL "$URL" -o "${TMPDIR}/${ARCHIVE}"
curl -fsSL "$CHECKSUMS_URL" -o "${TMPDIR}/checksums.txt"

# Verify checksum
cd "$TMPDIR"
EXPECTED=$(grep "${ARCHIVE}" checksums.txt | awk '{print $1}')
if [ -n "$EXPECTED" ]; then
  if command -v sha256sum >/dev/null 2>&1; then
    ACTUAL=$(sha256sum "${ARCHIVE}" | awk '{print $1}')
  elif command -v shasum >/dev/null 2>&1; then
    ACTUAL=$(shasum -a 256 "${ARCHIVE}" | awk '{print $1}')
  else
    echo "Warning: no sha256sum or shasum found, skipping checksum verification"
    ACTUAL="$EXPECTED"
  fi

  if [ "$ACTUAL" != "$EXPECTED" ]; then
    echo "Checksum verification failed!"
    echo "  Expected: $EXPECTED"
    echo "  Got:      $ACTUAL"
    exit 1
  fi
  echo "Checksum verified."
fi

# Extract
tar -xzf "${ARCHIVE}"

# Install
chmod +x "${BINARY}"
if [ -w "$INSTALL_DIR" ]; then
  mv "${BINARY}" "${INSTALL_DIR}/${BINARY}"
else
  sudo mv "${BINARY}" "${INSTALL_DIR}/${BINARY}"
fi

echo "Installed agend ${LATEST} to ${INSTALL_DIR}/${BINARY}"
echo ""
echo "Get started:"
echo "  agend login"
echo "  agend setup claude"
echo ""
echo "Update later with:"
echo "  agend update"
