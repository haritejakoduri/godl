#!/usr/bin/env bash
# Builds dist/godl-setup.exe: a one-click Windows installer with godl.exe
# embedded inside it. Two stages, since go:embed can't reach outside the
# embedding package's own directory tree:
#   1. cross-compile godl.exe itself into installer/payload/ (embedded input)
#   2. build the installer, which go:embeds that payload
#
# Both exes get a Windows version-info resource (file/product name,
# version, copyright) embedded via go-winres before compiling. A bare
# Go-compiled exe with no version resource is more likely to get
# heuristically flagged by Defender/SmartScreen than one with normal PE
# metadata; this doesn't replace code signing (nothing does, short of an
# EV cert), but it's a real reduction in false-positive risk.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_DIR"

VERSION="$(grep -oP '(?<=Version = ")[^"]+' internal/version/version.go)"
if [ -z "$VERSION" ]; then
	echo "error: couldn't read version from internal/version/version.go" >&2
	exit 1
fi

GO_WINRES="go run github.com/tc-hib/go-winres@v0.3.3"
cleanup() { rm -f ./*_windows_amd64.syso installer/*_windows_amd64.syso; }
trap cleanup EXIT

echo "1/2 Building godl.exe payload ($VERSION)..."
$GO_WINRES simply --arch amd64 --out godl_rsrc --manifest cli \
	--file-version "$VERSION" --product-version "$VERSION" \
	--file-description "godl - terminal download manager" \
	--product-name "godl" --original-filename "godl.exe" \
	--copyright "MIT License" >/dev/null
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o installer/payload/godl.exe .
rm -f ./godl_rsrc_windows_amd64.syso

OUT="dist/godl-setup-${VERSION}.exe"
echo "2/2 Building $OUT..."
mkdir -p dist
(
	cd installer
	$GO_WINRES simply --arch amd64 --out installer_rsrc --manifest cli \
		--file-version "$VERSION" --product-version "$VERSION" \
		--file-description "godl installer" \
		--product-name "godl" --original-filename "godl-setup.exe" \
		--copyright "MIT License" >/dev/null
)
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o "$OUT" ./installer

echo
echo "Built $OUT ($(du -h "$OUT" | cut -f1))"
