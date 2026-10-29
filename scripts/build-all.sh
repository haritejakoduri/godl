#!/usr/bin/env bash
# Builds every release artifact that Linux can produce, into dist/, all
# reading the same version from internal/version/version.go: raw
# cross-compiled binaries for each Linux architecture and for Windows
# on ARM, plus the one-click installer for Windows (godl-setup.exe,
# which embeds its own windows/amd64 build — no separate raw x86 Windows
# binary published, since the installer is a strictly better artifact
# for anyone on Windows) and the Linux .deb and .rpm packages.
#
# Everything here is CGO_ENABLED=0, so every architecture is an
# ordinary cross-compile from this one machine and every output is a
# single static file with no runtime dependencies. That is also what
# makes godl work under Termux on Android, which has no glibc to link
# against: the linux/arm64 binary runs there as-is. (GOOS=android
# proper needs the NDK and cgo, so it is not built.)
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

echo "Building godl $VERSION (Linux + Windows-on-ARM binaries, Windows installer, .deb, .rpm)..."
echo

mkdir -p dist

# goos goarch goarm suffix — goarm is empty except for 32-bit ARM,
# where v7 is what Raspberry Pi OS and Debian armhf mean.
raw() {
	local goos=$1 goarch=$2 goarm=$3 suffix=$4
	local out="dist/godl-${VERSION}-${suffix}"
	echo "-> $out"
	if [ -n "$goarm" ]; then
		CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" GOARM="$goarm" \
			go build -trimpath -ldflags="-s -w" -o "$out" .
	else
		CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
			go build -trimpath -ldflags="-s -w" -o "$out" .
	fi
}

raw linux amd64 "" linux-amd64
raw linux arm64 "" linux-arm64
raw linux arm 7 linux-armv7
# Windows on ARM can emulate the x64 installer, but slowly and with a
# larger memory footprint; a native build costs one more cross-compile.
raw windows arm64 "" windows-arm64.exe

echo
"$SCRIPT_DIR/build-windows-installer.sh"

echo
"$SCRIPT_DIR/build-deb.sh" amd64
echo
"$SCRIPT_DIR/build-deb.sh" arm64

echo
"$SCRIPT_DIR/build-rpm.sh" x86_64
echo
"$SCRIPT_DIR/build-rpm.sh" aarch64

echo
echo "All godl $VERSION artifacts:"
ls -lh dist/ | awk 'NR>1 {printf "  %-28s %s\n", $NF, $5}'
