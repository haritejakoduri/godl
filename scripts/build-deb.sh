#!/usr/bin/env bash
# Builds a .deb package: double-click (or `sudo apt install ./godl_*.deb`)
# to install godl system-wide as /usr/bin/godl. Version comes straight
# from internal/version/version.go, so re-running this after bumping
# that gives dpkg/apt everything they need to treat it as an upgrade —
# no bespoke update-detection code required, that's just how apt works.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_DIR"

for tool in dpkg-deb fakeroot; do
	if ! command -v "$tool" >/dev/null 2>&1; then
		echo "error: $tool not found (Debian/Ubuntu: sudo apt install dpkg-dev fakeroot)" >&2
		exit 1
	fi
done

VERSION="$(grep -oP '(?<=Version = ")[^"]+' internal/version/version.go)"
if [ -z "$VERSION" ]; then
	echo "error: couldn't read version from internal/version/version.go" >&2
	exit 1
fi

STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT
chmod 0755 "$STAGE" # mktemp -d defaults to 0700; a package root should read like a normal dir

echo "Building godl $VERSION for linux/amd64..."
mkdir -p dist "$STAGE/DEBIAN" "$STAGE/usr/bin"
# Built straight into the staging tree, not dist/ — this is a packaging
# intermediate, not a release artifact someone would download directly
# (that's dist/godl-<version>-linux-amd64, from build-all.sh).
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o "$STAGE/usr/bin/godl" .
chmod 0755 "$STAGE/usr/bin/godl"

mkdir -p "$STAGE/DEBIAN"
install -m 0755 packaging/debian/postinst "$STAGE/DEBIAN/postinst"
install -m 0755 packaging/debian/prerm "$STAGE/DEBIAN/prerm"
install -m 0755 packaging/debian/postrm "$STAGE/DEBIAN/postrm"

SIZE_KB="$(du -k "$STAGE/usr/bin/godl" | cut -f1)"

cat >"$STAGE/DEBIAN/control" <<EOF
Package: godl
Version: $VERSION
Section: utils
Priority: optional
Architecture: amd64
Maintainer: godl <noreply@localhost>
Installed-Size: $SIZE_KB
Description: Terminal download manager for HTTP(S), BitTorrent, and yt-dlp
 godl downloads direct HTTP(S) links, torrents, and yt-dlp-supported
 social/media links through a background daemon, with a live TUI
 dashboard (godl status) and scriptable pause/resume/retry/cancel/list
 commands. Single static binary, no cgo.
EOF

OUT="dist/godl_${VERSION}_amd64.deb"
fakeroot dpkg-deb --build --root-owner-group "$STAGE" "$OUT"

echo
echo "Built $OUT ($(du -h "$OUT" | cut -f1))"
