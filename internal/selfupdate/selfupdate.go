// Package selfupdate updates godl's own binary in place, the same way
// internal/ytdlp and internal/ffmpeg keep their managed copies current:
// downloaded from GitHub Releases and verified against the digest
// GitHub itself computed for the asset, never trusted from the
// response body alone.
//
// Only platforms with a raw, prebuilt godl binary actually published
// (see scripts/build-all.sh: linux/amd64 and darwin/arm64 today) can
// self-update this way. Windows ships only an installer .exe (no
// standalone binary asset to swap in), so a Windows install has to go
// back to the Releases page instead; the same is true for any other
// unbuilt platform (darwin/amd64, linux/arm64). A godl installed from
// the Debian package also refuses to self-replace: dpkg owns that
// file, and silently swapping it out from under apt would leave the
// package database out of sync with what's actually on disk —
// `apt upgrade` is the correct path there.
package selfupdate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"godl/internal/ghrelease"
	"godl/internal/httpx"
	"godl/internal/version"
)

// httpClient fetches a release binary: generous whole-request cap, so
// a slow link still completes but a wedged connection does not hang.
var httpClient = httpx.Client(httpx.BinaryFetchTimeout)

var repo = "haritejakoduri/godl"
var releaseAPI = "https://api.github.com/repos/" + repo + "/releases/latest"

var releaseBase = func(tag string) string {
	return "https://github.com/" + repo + "/releases/download/" + tag + "/"
}

// assetName returns the raw binary asset name scripts/build-all.sh
// publishes for the current platform at the given version, or false if
// this platform doesn't get one at all. A var (not a plain func), like
// releaseAPI/releaseBase/osExecutable above, purely so a test can force
// the "no asset for this platform" (Windows, in practice) path on
// whatever platform actually runs the test suite.
var assetName = func(ver string) (string, bool) {
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		return fmt.Sprintf("godl-%s-linux-amd64", ver), true
	case "darwin/arm64":
		return fmt.Sprintf("godl-%s-darwin-arm64", ver), true
	default:
		return "", false
	}
}

// dpkgManagedPath is the fixed location godl's .deb package installs
// to (see scripts/build-deb.sh and the README's Linux install/uninstall
// instructions) — the one path self-update refuses to touch.
const dpkgManagedPath = "/usr/bin/godl"

func dpkgManaged(exePath string) bool {
	return runtime.GOOS == "linux" && exePath == dpkgManagedPath
}

// Result describes what ForceUpdate did, for callers (godl update) to
// report to the user — self-update has more terminal outcomes than a
// plain updated-or-not, unlike ytdlp/ffmpeg's ForceUpdate: there's a
// real, expected chance the current platform or install method just
// isn't self-updatable at all, which isn't a failure.
type Result int

const (
	AlreadyLatest Result = iota
	Updated
	Unsupported
	ManagedInstall
)

// ForceUpdate checks the latest godl release against the running
// binary's version and, if newer, downloads and verifies the
// platform's release asset and replaces the current executable with it
// in place. That replace-in-place is safe even while this same binary
// is the one currently running it: on Linux/macOS, renaming a new file
// over an in-use executable's path doesn't disturb the running
// process at all (it keeps its already-open file description
// regardless of what the directory entry now points to) — only the
// *next* invocation, a new process, sees the new file. That's also
// exactly why Windows isn't a target here even in principle: Windows
// won't let you replace a running .exe's file the same way, and there's
// no raw Windows binary asset published anyway (see the package doc).
var osExecutable = os.Executable

// ForceUpdate's second return value is the latest published version
// (e.g. "0.4.0"), whenever it was actually looked up — every outcome
// except the two that return before ever calling the GitHub API
// (osExecutable failing, or a dpkg-managed install refusing to touch
// itself at all). Callers use it to tell a genuinely unsupported
// platform/install ("here's what's new, go get it yourself") apart
// from one that's already current, even though both currently print
// through the same Unsupported/AlreadyLatest split — see cmd/update.go.
func ForceUpdate(ctx context.Context, progress func(string)) (result Result, latestVersion string, err error) {
	exePath, err := osExecutable()
	if err != nil {
		return Unsupported, "", fmt.Errorf("locating the running godl binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exePath); err == nil {
		exePath = resolved
	}

	if dpkgManaged(exePath) {
		return ManagedInstall, "", nil
	}

	latestTag, err := ghrelease.TagName(ctx, releaseAPI)
	if err != nil {
		return Unsupported, "", fmt.Errorf("checking the latest godl release: %w", err)
	}
	latestVersion = strings.TrimPrefix(latestTag, "v")
	if latestVersion == version.Version {
		return AlreadyLatest, latestVersion, nil
	}

	asset, ok := assetName(latestVersion)
	if !ok {
		return Unsupported, latestVersion, nil
	}

	report(progress, "fetching godl "+latestVersion+" release checksum...")
	wantHex, err := ghrelease.AssetDigest(ctx, releaseAPI, asset)
	if err != nil {
		return Unsupported, latestVersion, fmt.Errorf("looking up godl's published checksum (refusing to update unverified): %w", err)
	}

	report(progress, "downloading godl "+latestVersion+"...")
	if err := ghrelease.DownloadVerified(ctx, httpClient, releaseBase(latestTag)+asset, exePath, ".new", wantHex); err != nil {
		return Unsupported, latestVersion, fmt.Errorf("updating godl: %w", err)
	}
	report(progress, "godl updated to "+latestVersion+" — already-running commands (godl status, godl serve, ...) keep using the old binary until restarted")
	return Updated, latestVersion, nil
}

func report(progress func(string), msg string) {
	if progress != nil {
		progress(msg)
	}
}
