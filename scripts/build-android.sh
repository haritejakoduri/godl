#!/usr/bin/env bash
# Builds godl for Android into dist/ as a development build.
#
# Development build is meant literally, and the "-dev" in the file name
# is there so nobody has to take anyone's word for it: this is not
# released, not supported, and has never been run on a phone by anyone.
# The release workflow does not call this script, and no Android
# artifact is published. A bare Android executable is of no use on its own —
# Android gives you no shell to run it from. What it is for is the two
# things that would consume it later: gomobile, which turns this
# package into a library an Android app can start, and anything
# providing a Linux-like userland. (Termux is not that consumer: it
# runs the ordinary linux/arm64 binary, which needs none of this.)
#
# It exists so that "does godl still compile for Android" has an
# answer that does not depend on someone remembering to check.
#
# Two things make an Android build different from the Linux one:
#
#   * Go satisfies the "linux" build constraint on Android as well, so
#     every platform file written for Linux is selected here too. For
#     the tray that is fatal — fyne.io/systray has no Android backend —
#     which is why its build tag names android explicitly. The rest of
#     the Linux platform glue (desktop entries, autostart, the terminal
#     dashboard) is selected and inert: it compiles, and nothing on a
#     phone would ever reach it. Worth revisiting when there is a real
#     app to test against, rather than guessing now.
#
#   * github.com/wlynxg/anet, which the torrent stack depends on for
#     enumerating network interfaces on Android, reaches into the
#     standard library with //go:linkname. Go rejects that by default
#     since 1.23, so the link needs -checklinkname=0. Without it the
#     build fails at the very last step with "invalid reference to
#     net.zoneCache", which reads like a compiler bug rather than a
#     missing flag.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_DIR"

VERSION="$(grep -oP '(?<=Version = ")[^"]+' internal/version/version.go)"
if [ -z "$VERSION" ]; then
	echo "error: couldn't read version from internal/version/version.go" >&2
	exit 1
fi

mkdir -p dist

echo "Building godl $VERSION for Android (development build)..."
echo

# arm64 only, and not an oversight: every other Android architecture
# — arm, amd64, 386 — requires external linking, which means the NDK,
# which this does not assume. arm64 is also the one that matters, since
# Google Play has required 64-bit since 2019. Whoever builds a real app
# will have the NDK and can add the rest then, with something to test
# them on.
for arch in arm64; do
	OUT="dist/godl-${VERSION}-android-${arch}-dev"
	echo "-> $OUT"
	export CGO_ENABLED=0 GOOS=android GOARCH="$arch"

	# Asked under this GOOS/GOARCH, not the host's, because which tray
	# file is selected is exactly what they decide. The stub is the
	# right answer here: a phone has no notification area, and the real
	# one does not link. If this ever resolves to app_supported.go the
	# build fails much later and far less clearly.
	if ! go list -f '{{join .GoFiles " "}}' ./tray/ | grep -q app_unsupported.go; then
		echo "error: android/$arch selected the real tray, not the stub" >&2
		echo "       (check the build tags in tray/app_supported.go)" >&2
		exit 1
	fi

	go build -trimpath -ldflags="-s -w -checklinkname=0" -o "$OUT" .
done
unset CGO_ENABLED GOOS GOARCH

echo
echo "Android artifacts (development builds — unreleased, untested on a device):"
ls -lh dist/ | grep android | awk '{printf "  %-38s %s\n", $NF, $5}'
