#!/bin/sh
# Installs the latest cowboy release for this computer (macOS or Linux).
#
#   curl -fsSL https://github.com/sgracia13/Cowboy/releases/latest/download/install.sh | sh
#
# Puts the binary in /usr/local/bin if writable, otherwise ~/.local/bin.
# Set COWBOY_VERSION=v0.1.0-preview to pin a version, or COWBOY_DIR=/some/dir.
set -eu

REPO="sgracia13/Cowboy"
VERSION="${COWBOY_VERSION:-latest}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$os" in
  darwin|linux) ;;
  *) echo "cowboy: unsupported system '$os'. On Windows, download cowboy-windows-amd64.exe from https://github.com/$REPO/releases" >&2; exit 1 ;;
esac
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) echo "cowboy: unsupported processor '$arch'" >&2; exit 1 ;;
esac
name="cowboy-$os-$arch"

if [ "$VERSION" = latest ]; then
  base="https://github.com/$REPO/releases/latest/download"
else
  base="https://github.com/$REPO/releases/download/$VERSION"
fi
base="${COWBOY_BASE_URL:-$base}" # for testing against a local copy

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO "$2" "$1"; }
else
  echo "cowboy: needs curl or wget" >&2; exit 1
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "downloading $name ($VERSION)..."
fetch "$base/$name" "$tmp/cowboy"
fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS"

want=$(grep " $name\$" "$tmp/SHA256SUMS" | cut -d' ' -f1)
if command -v sha256sum >/dev/null 2>&1; then
  got=$(sha256sum "$tmp/cowboy" | cut -d' ' -f1)
else
  got=$(shasum -a 256 "$tmp/cowboy" | cut -d' ' -f1)
fi
if [ -z "$want" ] || [ "$want" != "$got" ]; then
  echo "cowboy: checksum mismatch, not installing" >&2; exit 1
fi
chmod +x "$tmp/cowboy"

dir="${COWBOY_DIR:-}"
if [ -z "$dir" ]; then
  if [ -w /usr/local/bin ]; then dir=/usr/local/bin; else dir="$HOME/.local/bin"; fi
fi
mkdir -p "$dir"
mv "$tmp/cowboy" "$dir/cowboy"
# downloads from the internet are quarantined on macOS; this build isn't signed yet
if [ "$os" = darwin ] && command -v xattr >/dev/null 2>&1; then
  xattr -d com.apple.quarantine "$dir/cowboy" 2>/dev/null || true
fi

echo "installed cowboy to $dir/cowboy"
case ":$PATH:" in
  *":$dir:"*) ;;
  *) echo "add it to your PATH:  export PATH=\"$dir:\$PATH\"" ;;
esac
echo "try it:  cowboy demo   (then open http://localhost:8080)"
