#!/usr/bin/env bash
# Builds dist/godl-setup.exe: a one-click Windows installer with godl.exe
# embedded inside it. Two stages, since go:embed can't reach outside the
# embedding package's own directory tree:
#   1. cross-compile godl.exe itself into installer/payload/ (embedded input)
#   2. build the installer, which go:embeds that payload
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_DIR"

VERSION="$(grep -oP '(?<=Version = ")[^"]+' internal/version/version.go)"
if [ -z "$VERSION" ]; then
	echo "error: couldn't read version from internal/version/version.go" >&2
	exit 1
fi

echo "1/2 Building godl.exe payload ($VERSION)..."
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o installer/payload/godl.exe .

OUT="dist/godl-setup-${VERSION}.exe"
echo "2/2 Building $OUT..."
mkdir -p dist
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o "$OUT" ./installer

echo
echo "Built $OUT ($(du -h "$OUT" | cut -f1))"
