#!/usr/bin/env bash
# Builds a .rpm package: `sudo dnf install ./godl-<version>-1.*.rpm`
# (or double-click in a file manager) installs godl system-wide as
# /usr/bin/godl. Version comes straight from internal/version/version.go,
# so re-running this after bumping that gives dnf everything it needs
# to treat it as an upgrade — no bespoke update-detection code
# required, that's just how dnf works.
#
# Takes an RPM architecture as its one argument (x86_64 by default).
# godl is CGO_ENABLED=0 throughout, so a package for another
# architecture is an ordinary cross-compile needing no toolchain beyond
# Go, and rpmbuild --target labels the result without compiling
# anything — the spec only installs an already-built binary.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_DIR"

RPM_ARCH="${1:-x86_64}"
case "$RPM_ARCH" in
x86_64) GO_ARCH=amd64 ;;
aarch64) GO_ARCH=arm64 ;;
*)
	echo "error: unknown RPM architecture $RPM_ARCH (want x86_64 or aarch64)" >&2
	exit 1
	;;
esac

if ! command -v rpmbuild >/dev/null 2>&1; then
	echo "error: rpmbuild not found (Fedora/RHEL: sudo dnf install rpm-build; Debian/Ubuntu: sudo apt install rpm)" >&2
	exit 1
fi

VERSION="$(grep -oP '(?<=Version = ")[^"]+' internal/version/version.go)"
if [ -z "$VERSION" ]; then
	echo "error: couldn't read version from internal/version/version.go" >&2
	exit 1
fi
# RPM's Version field can't contain a hyphen; godl's own versions never
# do, but fail loudly instead of handing rpmbuild something it would
# otherwise choke on with a much less obvious error.
case "$VERSION" in
*-*)
	echo "error: version $VERSION contains a hyphen, which isn't valid in an RPM Version field" >&2
	exit 1
	;;
esac

TOPDIR="$(mktemp -d)"
trap 'rm -rf "$TOPDIR"' EXIT
mkdir -p "$TOPDIR"/{BUILD,RPMS,SOURCES,SPECS,SRPMS,BUILDROOT}

echo "Building godl $VERSION for linux/$GO_ARCH (RPM $RPM_ARCH)..."
mkdir -p dist
# Built straight into rpmbuild's SOURCES dir, not dist/ — this is a
# packaging intermediate, not a release artifact someone would
# download directly (that's dist/godl-<version>-linux-amd64, from
# build-all.sh).
CGO_ENABLED=0 GOOS=linux GOARCH="$GO_ARCH" go build -trimpath -ldflags="-s -w" -o "$TOPDIR/SOURCES/godl" .
chmod 0755 "$TOPDIR/SOURCES/godl"
cp LICENSE "$TOPDIR/SOURCES/LICENSE"
# The menu entry and its icons, which the spec's %install stages into
# /usr/share. Copied flat because %_sourcedir has no subdirectories.
cp packaging/desktop/godl.desktop "$TOPDIR/SOURCES/godl.desktop"
cp tray/icon/app*.png tray/icon/godl.svg "$TOPDIR/SOURCES/"

rpmbuild --define "_topdir $TOPDIR" \
	--define "_version $VERSION" \
	--target "$RPM_ARCH" \
	-bb packaging/rpm/godl.spec

BUILT_RPM="$(find "$TOPDIR/RPMS" -name '*.rpm' -print -quit)"
if [ -z "$BUILT_RPM" ]; then
	echo "error: rpmbuild didn't produce an .rpm" >&2
	exit 1
fi

OUT="dist/godl-${VERSION}-1.${RPM_ARCH}.rpm"
cp "$BUILT_RPM" "$OUT"

echo
echo "Built $OUT ($(du -h "$OUT" | cut -f1))"
