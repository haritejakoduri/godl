#!/usr/bin/env bash
# Builds every release artifact into dist/, all reading the same
# version from internal/version/version.go: raw cross-compiled
# binaries for Linux/macOS, plus the one-click installers for Windows
# (godl-setup.exe, which embeds its own windows/amd64 build — no
# separate raw Windows binary published, since the installer is a
# strictly better artifact for anyone on Windows) and Linux (.deb).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_DIR"

VERSION="$(grep -oP '(?<=Version = ")[^"]+' internal/version/version.go)"
if [ -z "$VERSION" ]; then
	echo "error: couldn't read version from internal/version/version.go" >&2
	exit 1
fi

echo "Building godl $VERSION for all platforms..."
echo

mkdir -p dist

OUT_LINUX="dist/godl-${VERSION}-linux-amd64"
echo "-> $OUT_LINUX"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o "$OUT_LINUX" .

OUT_DARWIN="dist/godl-${VERSION}-darwin-arm64"
echo "-> $OUT_DARWIN"
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o "$OUT_DARWIN" .

echo
"$SCRIPT_DIR/build-windows-installer.sh"

echo
"$SCRIPT_DIR/build-deb.sh"

echo
echo "All godl $VERSION artifacts:"
ls -lh dist/ | awk 'NR>1 {printf "  %-28s %s\n", $NF, $5}'
