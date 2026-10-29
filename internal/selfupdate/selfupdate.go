// Package selfupdate updates godl's own binary in place, the same way
// internal/ytdlp and internal/ffmpeg keep their managed copies current:
// downloaded from GitHub Releases and verified against the digest
// GitHub itself computed for the asset, never trusted from the
// response body alone.
//
// Only platforms with a raw, prebuilt godl binary actually published
// can self-update this way; rawAssets below is that list, and a test
// checks it against the build scripts so the two cannot drift apart
// again. Windows is deliberately not in it even though a
// windows-arm64 binary is now published: a Windows install updates
// through its installer, and replacing a running .exe in place is a
// different problem, so all of Windows goes back to the Releases page.
//
// A godl installed from the .deb or .rpm package never has its binary
// swapped directly: dpkg/rpm owns that file, and silently replacing it
// out from under apt/dnf would leave the package database out of sync
// with what's actually on disk. An .rpm install updates through the
// package manager instead — the release's .rpm is downloaded and
// verified the same way, then handed to dnf (see updateRPM) — while a
// .deb install is still just pointed at apt.
package selfupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"godl/internal/ghrelease"
	"godl/internal/httpx"
	progresspkg "godl/internal/progress"
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

// rawAssets maps a platform to the suffix of the raw binary asset
// published for it — scripts/build-all.sh for the Linux ones,
// scripts/build-darwin.sh for the macOS ones.
//
// A map rather than a switch so a test can enumerate it and compare it
// against those scripts. That test exists because this list silently
// fell behind once already: darwin/amd64 started being published and
// nothing here was changed, so every Intel Mac was told there was no
// update for its platform.
var rawAssets = map[string]string{
	"linux/amd64":  "linux-amd64",
	"linux/arm64":  "linux-arm64",
	"linux/arm":    "linux-armv7",
	"darwin/amd64": "darwin-amd64",
	"darwin/arm64": "darwin-arm64",
}

// assetName returns the raw binary asset name published for the
// current platform at the given version, or false if this platform
// doesn't get one at all. A var (not a plain func), like
// releaseAPI/releaseBase/osExecutable above, purely so a test can force
// the "no asset for this platform" (Windows, in practice) path on
// whatever platform actually runs the test suite.
var assetName = func(ver string) (string, bool) {
	suffix, ok := rawAssets[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return "", false
	}
	return fmt.Sprintf("godl-%s-%s", ver, suffix), true
}

// packageManagedPath is the fixed location both godl's .deb and .rpm
// packages install to (see scripts/build-deb.sh, scripts/build-rpm.sh,
// and the README's Linux install/uninstall instructions) — the one
// path self-update never replaces directly. The two package formats
// can't be told apart by path alone (both use /usr/bin/godl); rpmOwned
// below is what separates them.
const packageManagedPath = "/usr/bin/godl"

func packageManaged(exePath string) bool {
	return runtime.GOOS == "linux" && exePath == packageManagedPath
}

// rpmPackageName is the Name field in packaging/rpm/godl.spec.
const rpmPackageName = "godl"

// rpmArches maps Go's name for an architecture to RPM's, for the ones
// scripts/build-rpm.sh is run for by scripts/build-all.sh.
var rpmArches = map[string]string{
	"amd64": "x86_64",
	"arm64": "aarch64",
}

// rpmAssetName returns the .rpm asset name scripts/build-rpm.sh
// publishes at the given version, or false on an architecture no .rpm
// is built for.
var rpmAssetName = func(ver string) (string, bool) {
	arch, ok := rpmArches[runtime.GOARCH]
	if !ok {
		return "", false
	}
	return fmt.Sprintf("godl-%s-1.%s.rpm", ver, arch), true
}

// rpmOwned reports whether exePath belongs to godl's installed .rpm
// package, by asking the rpm database itself. Anything else — no rpm
// tool at all, or one that doesn't know the file (a Debian box that
// merely has rpm installed) — is a .deb install as far as ForceUpdate
// is concerned. A var so a test can stand in for the rpm database.
var rpmOwned = func(ctx context.Context, exePath string) bool {
	out, err := exec.CommandContext(ctx, "rpm", "-qf", "--queryformat", "%{NAME}", exePath).Output()
	return err == nil && string(out) == rpmPackageName
}

// installRPM hands a downloaded, already-verified .rpm to the system
// package manager, so the upgrade lands in the rpm database like any
// other. dnf where there is one, plain `rpm -U` otherwise; run through
// sudo (or pkexec, on a desktop without sudo) unless godl is already
// root. The child gets godl's own terminal, so sudo can prompt for a
// password and dnf's transaction output shows as it happens. A var so
// tests never touch the real package manager.
var installRPM = func(ctx context.Context, rpmPath string) error {
	argv := []string{"rpm", "-U", rpmPath}
	if _, err := exec.LookPath("dnf"); err == nil {
		argv = []string{"dnf", "install", "-y", rpmPath}
	}
	if os.Geteuid() != 0 {
		elevate := ""
		for _, tool := range []string{"sudo", "pkexec"} {
			if _, err := exec.LookPath(tool); err == nil {
				elevate = tool
				break
			}
		}
		if elevate == "" {
			return fmt.Errorf("installing a package needs root, and neither sudo nor pkexec is available — re-run \"godl update\" as root")
		}
		argv = append([]string{elevate}, argv...)
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
	}
	return nil
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
// (osExecutable failing, or a .deb install, which is left to apt
// entirely). Callers use it to tell a genuinely unsupported
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

	managed := packageManaged(exePath)
	if managed && !rpmOwned(ctx, exePath) {
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

	if managed {
		return updateRPM(ctx, latestTag, latestVersion, progress)
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
	bar := progresspkg.New("  "+asset, progress)
	if err := ghrelease.DownloadVerified(ctx, httpClient, releaseBase(latestTag)+asset, exePath, ".new", wantHex, bar.Func()); err != nil {
		return Unsupported, latestVersion, fmt.Errorf("updating godl: %w", err)
	}
	report(progress, updatedMessage(latestVersion))
	return Updated, latestVersion, nil
}

// updateRPM is ForceUpdate's path for an .rpm install: the release's
// .rpm is downloaded into a throwaway directory, verified against
// GitHub's published digest exactly like the raw binary is, and only
// then given to the package manager. The same holds for the running
// process as in the raw-binary case — rpm replaces /usr/bin/godl by
// rename too, and the spec's %preun leaves the daemon alone on an
// upgrade.
func updateRPM(ctx context.Context, latestTag, latestVersion string, progress func(string)) (Result, string, error) {
	asset, ok := rpmAssetName(latestVersion)
	if !ok {
		return Unsupported, latestVersion, nil
	}

	report(progress, "fetching godl "+latestVersion+" release checksum...")
	wantHex, err := ghrelease.AssetDigest(ctx, releaseAPI, asset)
	if err != nil {
		return Unsupported, latestVersion, fmt.Errorf("looking up godl's published checksum (refusing to update unverified): %w", err)
	}

	dir, err := os.MkdirTemp("", "godl-update-")
	if err != nil {
		return Unsupported, latestVersion, fmt.Errorf("updating godl: %w", err)
	}
	defer os.RemoveAll(dir)
	rpmPath := filepath.Join(dir, asset)

	report(progress, "downloading "+asset+"...")
	bar := progresspkg.New("  "+asset, progress)
	if err := ghrelease.DownloadVerified(ctx, httpClient, releaseBase(latestTag)+asset, rpmPath, ".part", wantHex, bar.Func()); err != nil {
		return Unsupported, latestVersion, fmt.Errorf("updating godl: %w", err)
	}

	report(progress, "installing "+asset+" (needs root)...")
	if err := installRPM(ctx, rpmPath); err != nil {
		return Unsupported, latestVersion, fmt.Errorf("updating godl: %w", err)
	}
	report(progress, updatedMessage(latestVersion))
	return Updated, latestVersion, nil
}

func updatedMessage(latestVersion string) string {
	return "godl updated to " + latestVersion + " — open godl windows (godl status, godl serve, ...) keep using the old version until reopened; the background daemon restarts by itself on the next godl command"
}

func report(progress func(string), msg string) {
	if progress != nil {
		progress(msg)
	}
}
