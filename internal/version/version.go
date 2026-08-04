// Package version holds godl's version string, bumped by hand for each
// release (there's no git-tag/CI machinery to derive it from here).
package version

// Version is godl's release version. Package builds (scripts/install.sh,
// scripts/build-deb.sh, scripts/build-windows-installer.sh) read this
// via `godl --version` to decide whether a run is a fresh install or an
// upgrade; dpkg/apt compare it directly against Debian package Version
// fields.
var Version = "0.1.0"
