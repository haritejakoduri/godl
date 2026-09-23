#!/usr/bin/env bash
# Builds the macOS release binaries into dist/.
#
# Separate from build-all.sh, and required to run on a Mac, because the
# system tray needs Cocoa: tray/app_supported.go is only selected for
# darwin when cgo is on, and cgo for macOS needs the macOS SDK and
# toolchain. A CGO_ENABLED=0 cross-compile from Linux still produces a
# working godl — it just has no tray — so this exists to make sure the
# published macOS build is the one with it.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_DIR"

if [ "$(go env GOOS)" != "darwin" ]; then
	echo "error: run this on macOS — a cgo-enabled darwin build needs the macOS toolchain" >&2
	exit 1
fi

VERSION="$(grep -oE 'Version = "[^"]+"' internal/version/version.go | head -1 | cut -d'"' -f2)"
if [ -z "$VERSION" ]; then
	echo "error: couldn't read version from internal/version/version.go" >&2
	exit 1
fi

mkdir -p dist

# Both architectures are built natively-ish on the same host: the
# runner is Apple silicon, and clang cross-compiles to x86_64 against
# the same SDK, so Intel Macs get a build with a tray too.
for arch in arm64 amd64; do
	OUT="dist/godl-${VERSION}-darwin-${arch}"
	echo "-> $OUT"
	export CGO_ENABLED=1 GOOS=darwin GOARCH="$arch"

	# Asked under the same GOOS/GOARCH/CGO_ENABLED as the build below,
	# not the host's: which tray file is selected is exactly what those
	# settings decide, so checking with anything else proves nothing.
	if ! go list -f '{{join .GoFiles " "}}' ./tray/ | grep -q app_supported.go; then
		echo "error: darwin/$arch resolves to the tray stub, not the real one" >&2
		echo "       (cgo unavailable? this needs Xcode command line tools)" >&2
		exit 1
	fi

	go build -trimpath -ldflags="-s -w" -o "$OUT" .
done
unset CGO_ENABLED GOOS GOARCH

echo
echo "macOS artifacts:"
ls -lh dist/ | grep darwin | awk '{printf "  %-32s %s\n", $NF, $5}'
