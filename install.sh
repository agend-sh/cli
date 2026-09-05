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

AGEND_INSTALL_TMP=$(mktemp -d)
trap 'rm -rf "$AGEND_INSTALL_TMP"' EXIT

# Download archive and checksums
curl -fsSL "$URL" -o "${AGEND_INSTALL_TMP}/${ARCHIVE}"
curl -fsSL "$CHECKSUMS_URL" -o "${AGEND_INSTALL_TMP}/checksums.txt"

# Bootstrap a fixed verifier from a separately pinned Sigstore release.
# Never execute the downloaded agend binary to verify itself.
COSIGN_VERSION=v3.1.3
case "${OS}/${ARCH}" in
  darwin/amd64) COSIGN_SHA=2347488e5d5b25336644024dfeca5601b190e91197a71a917bda44744aff106c ;;
  darwin/arm64) COSIGN_SHA=5cf948c2f4dfe59687bdd0b8523709067383e03982cc543475c8a7dc70e92a76 ;;
  linux/amd64) COSIGN_SHA=4629c757b7618056f8ddd7e2625ae9fdd94c0372a65049520bc7d9df9efc7f71 ;;
  linux/arm64) COSIGN_SHA=c5d324e091826b0d7a78eb16fef316450b4eb9aaec045611c08ba06f5e73220a ;;
esac
sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    echo "Error: sha256sum or shasum is required." >&2
    return 1
  fi
}
cd "$AGEND_INSTALL_TMP"
curl -fsSL "https://github.com/sigstore/cosign/releases/download/${COSIGN_VERSION}/cosign-${OS}-${ARCH}" -o cosign
if [ "$(sha256_file cosign)" != "$COSIGN_SHA" ]; then
  echo "Error: signature verifier checksum mismatch — refusing to install." >&2
  exit 1
fi
chmod 700 cosign
curl -fsSL "${CHECKSUMS_URL}.sigstore.json" -o checksums.txt.sigstore.json
./cosign verify-blob --bundle checksums.txt.sigstore.json \
  --certificate-identity "https://github.com/agend-sh/cli/.github/workflows/release.yml@refs/tags/${LATEST}" \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com checksums.txt

# Only a signed checksum manifest can authorize extraction or installation.
EXPECTED=$(awk -v asset="$ARCHIVE" '$2 == asset { print $1 }' checksums.txt)
ACTUAL=$(sha256_file "$ARCHIVE")
if [ "$ACTUAL" != "$EXPECTED" ]; then
  echo "Error: archive checksum missing or mismatched — refusing to install." >&2
  exit 1
fi
echo "Release signature and checksum verified."

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
