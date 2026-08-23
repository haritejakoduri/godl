// Package version holds godl's version string, bumped by hand for each
// release — nothing derives it automatically. Bumping it and merging
// to master is what triggers .github/workflows/release.yml to publish
// a new GitHub Release: that workflow checks whether a git tag for the
// current Version already exists, and only builds/publishes when it
// doesn't, so a plain unrelated commit to master is a no-op for it.
package version

// Version is godl's release version. Package builds (scripts/install.sh,
// scripts/build-deb.sh, scripts/build-windows-installer.sh) read this
// via `godl --version` to decide whether a run is a fresh install or an
// upgrade; dpkg/apt compare it directly against Debian package Version
// fields.
var Version = "0.1.1"
