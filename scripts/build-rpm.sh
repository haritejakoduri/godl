#!/usr/bin/env bash
# Builds a .rpm package: `sudo dnf install ./godl-<version>-1.*.rpm`
# (or double-click in a file manager) installs godl system-wide as
# /usr/bin/godl. Version comes straight from internal/version/version.go,
# so re-running this after bumping that gives dnf everything it needs
# to treat it as an upgrade — no bespoke update-detection code
# required, that's just how dnf works.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_DIR"

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

echo "Building godl $VERSION for linux/amd64..."
mkdir -p dist
# Built straight into rpmbuild's SOURCES dir, not dist/ — this is a
# packaging intermediate, not a release artifact someone would
# download directly (that's dist/godl-<version>-linux-amd64, from
# build-all.sh).
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o "$TOPDIR/SOURCES/godl" .
chmod 0755 "$TOPDIR/SOURCES/godl"
cp LICENSE "$TOPDIR/SOURCES/LICENSE"

rpmbuild --define "_topdir $TOPDIR" \
	--define "_version $VERSION" \
	-bb packaging/rpm/godl.spec

BUILT_RPM="$(find "$TOPDIR/RPMS" -name '*.rpm' -print -quit)"
if [ -z "$BUILT_RPM" ]; then
	echo "error: rpmbuild didn't produce an .rpm" >&2
	exit 1
fi

OUT="dist/godl-${VERSION}-1.x86_64.rpm"
cp "$BUILT_RPM" "$OUT"

echo
echo "Built $OUT ($(du -h "$OUT" | cut -f1))"
