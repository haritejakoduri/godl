#!/usr/bin/env bash
# Builds every release artifact that can be produced on Linux, all
# reading the same version from internal/version/version.go: a raw
# cross-compiled Linux binary, plus the one-click installers for
# Windows (godl-setup.exe, which embeds its own windows/amd64 build —
# no separate raw Windows binary published, since the installer is a
# strictly better artifact for anyone on Windows) and Linux (.deb).
#
# macOS is NOT built here: its tray needs cgo and the macOS toolchain,
# so it comes from build-darwin.sh run on a Mac. See that script.
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
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o "$OUT_LINUX" .

echo
"$SCRIPT_DIR/build-windows-installer.sh"

echo
"$SCRIPT_DIR/build-deb.sh"

echo
echo "All godl $VERSION artifacts:"
ls -lh dist/ | awk 'NR>1 {printf "  %-28s %s\n", $NF, $5}'
