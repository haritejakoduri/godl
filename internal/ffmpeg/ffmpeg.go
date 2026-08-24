// Package ffmpeg locates ffmpeg/ffprobe for yt-dlp to use when merging
// separately-downloaded video and audio streams. godl always installs
// and keeps its own static build from BtbN/FFmpeg-Builds — a
// long-running community source of prebuilt ffmpeg binaries (ffmpeg.org
// itself doesn't publish simple direct-download static binaries) widely
// used by other CLI tools for exactly this purpose — downloading it the
// first time it's actually needed and caching it under godl's data
// dir. PATH is never consulted, so godl's copy stays current on its
// own regardless of what else might be installed on the system.
package ffmpeg

import (
	"archive/tar"
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/ulikunitz/xz"

	"godl/internal/ghrelease"
	"godl/internal/paths"
)

const releaseBase = "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/"
const releaseAPI = "https://api.github.com/repos/BtbN/FFmpeg-Builds/releases/latest"

// staleAfter bounds how often Ensure bothers checking for a newer
// ffmpeg build on an already-installed copy. Less frequent than
// yt-dlp's own check: ffmpeg itself rarely needs to change for godl's
// use (merging yt-dlp's separately-downloaded streams) even when
// yt-dlp does.
const staleAfter = 7 * 24 * time.Hour

func checkedFile(binDir string) string { return filepath.Join(binDir, "ffmpeg.checked") }

// versionFile persists the archive digest that was actually installed.
// Unlike yt-dlp (a single direct-download binary matching its own
// release digest one-to-one), ffmpeg's asset is an archive containing
// several files, so what's installed can't be hashed back to the
// archive's digest — it has to be remembered instead.
func versionFile(binDir string) string { return filepath.Join(binDir, "ffmpeg.version") }

// isStale reports whether stampPath is missing or older than window —
// a missing stamp (never checked before) counts as stale.
func isStale(stampPath string, window time.Duration) bool {
	fi, err := os.Stat(stampPath)
	if err != nil {
		return true
	}
	return time.Since(fi.ModTime()) >= window
}

type archiveKind int

const (
	kindTarXz archiveKind = iota
	kindZip
)

type asset struct {
	file string
	kind archiveKind
}

func assetFor() (asset, error) {
	if runtime.GOARCH != "amd64" {
		return asset{}, fmt.Errorf("no prebuilt ffmpeg for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	switch runtime.GOOS {
	case "linux":
		return asset{file: "ffmpeg-master-latest-linux64-gpl.tar.xz", kind: kindTarXz}, nil
	case "windows":
		return asset{file: "ffmpeg-master-latest-win64-gpl.zip", kind: kindZip}, nil
	default:
		return asset{}, fmt.Errorf("no prebuilt ffmpeg for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

func binNames() (ffmpeg, ffprobe string) {
	if runtime.GOOS == "windows" {
		return "ffmpeg.exe", "ffprobe.exe"
	}
	return "ffmpeg", "ffprobe"
}

// Ensure returns a directory containing godl's own managed ffmpeg and
// ffprobe binaries — a previously auto-downloaded copy if present
// (checked for staleness and silently updated if a newer build
// exists), otherwise a freshly downloaded one. PATH is never
// consulted; see the package doc for why. progress, if non-nil, is
// called with human-readable status lines while a download is in
// flight.
func Ensure(ctx context.Context, progress func(string)) (dir string, err error) {
	dataDir, err := paths.DataDir()
	if err != nil {
		return "", err
	}
	binDir := filepath.Join(dataDir, "bin")
	ffmpegName, ffprobeName := binNames()

	if fi, err := os.Stat(filepath.Join(binDir, ffmpegName)); err == nil && !fi.IsDir() {
		maybeUpdate(ctx, binDir, ffmpegName, ffprobeName, progress)
		return binDir, nil
	}

	a, err := assetFor()
	if err != nil {
		return "", fmt.Errorf("ffmpeg not found on PATH, and godl can't auto-install it here: %w (install it yourself, e.g. via your OS package manager)", err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}

	report(progress, "fetching ffmpeg release checksum...")
	wantHex, err := ghrelease.AssetDigest(ctx, releaseAPI, a.file)
	if err != nil {
		return "", fmt.Errorf("looking up ffmpeg's published checksum (refusing to download unverified): %w", err)
	}

	report(progress, "ffmpeg not found; downloading a static build from "+releaseBase+a.file+" (one-time download, a couple hundred MB)")
	if err := installArchive(ctx, binDir, a, wantHex, map[string]string{ffmpegName: ffmpegName, ffprobeName: ffprobeName}); err != nil {
		return "", err
	}
	os.WriteFile(versionFile(binDir), []byte(wantHex), 0o644)
	os.WriteFile(checkedFile(binDir), nil, 0o644)

	report(progress, "ffmpeg installed to "+binDir)
	return binDir, nil
}

// installArchive downloads a from releaseBase, verifies it against
// wantHex, and extracts the files named in want (source basename ->
// dest basename in binDir) — the shared install path for both a
// first-time Ensure and a later checkAndUpdate.
func installArchive(ctx context.Context, binDir string, a asset, wantHex string, want map[string]string) error {
	url := releaseBase + a.file
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading ffmpeg: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading ffmpeg: unexpected status: %s", resp.Status)
	}

	// Always stage the full archive to disk before extracting anything
	// from it: zip needs random access to its central directory anyway,
	// and staging tar.xz too means the checksum is verified against the
	// complete archive *before* any of its contents are written out as
	// executables, rather than trusting a stream we're extracting live.
	ext := ".tar.xz"
	if a.kind == kindZip {
		ext = ".zip"
	}
	tmpArchive, err := os.CreateTemp(binDir, "ffmpeg-download-*"+ext)
	if err != nil {
		return err
	}
	tmpArchivePath := tmpArchive.Name()
	defer os.Remove(tmpArchivePath)
	gotHex, _, err := ghrelease.HashingCopy(tmpArchive, resp.Body)
	if err != nil {
		tmpArchive.Close()
		return fmt.Errorf("downloading ffmpeg: %w", err)
	}
	if err := tmpArchive.Close(); err != nil {
		return err
	}
	if err := ghrelease.Verify(gotHex, wantHex); err != nil {
		return err
	}

	switch a.kind {
	case kindTarXz:
		f, err := os.Open(tmpArchivePath)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := extractTarXz(f, binDir, want); err != nil {
			return fmt.Errorf("extracting ffmpeg: %w", err)
		}
	case kindZip:
		if err := extractZip(tmpArchivePath, binDir, want); err != nil {
			return fmt.Errorf("extracting ffmpeg: %w", err)
		}
	}
	return nil
}

// maybeUpdate checks, at most once every staleAfter, whether a newer
// ffmpeg build exists, and silently replaces the installed one if so.
// Any failure (offline, GitHub rate-limited, ...) is swallowed: a
// stale build is preferred over blocking (or breaking) a download on
// a failed connectivity check.
func maybeUpdate(ctx context.Context, binDir, ffmpegName, ffprobeName string, progress func(string)) {
	stamp := checkedFile(binDir)
	if !isStale(stamp, staleAfter) {
		return
	}
	os.WriteFile(stamp, nil, 0o644)
	checkAndUpdate(ctx, binDir, ffmpegName, ffprobeName, progress)
}

// checkAndUpdate compares the recorded release digest of the
// currently-installed build against the latest one and, if different,
// downloads and re-extracts the new archive in place. Returns whether
// an update actually happened.
func checkAndUpdate(ctx context.Context, binDir, ffmpegName, ffprobeName string, progress func(string)) (bool, error) {
	a, err := assetFor()
	if err != nil {
		return false, err
	}
	latestHex, err := ghrelease.AssetDigest(ctx, releaseAPI, a.file)
	if err != nil {
		return false, err
	}
	if current, err := os.ReadFile(versionFile(binDir)); err == nil && strings.EqualFold(strings.TrimSpace(string(current)), latestHex) {
		return false, nil
	}

	report(progress, "installing the latest ffmpeg build...")
	if err := installArchive(ctx, binDir, a, latestHex, map[string]string{ffmpegName: ffmpegName, ffprobeName: ffprobeName}); err != nil {
		return false, fmt.Errorf("updating ffmpeg: %w", err)
	}
	os.WriteFile(versionFile(binDir), []byte(latestHex), 0o644)
	report(progress, "ffmpeg updated to the latest build")
	return true, nil
}

// ForceUpdate immediately checks for (and installs) a newer ffmpeg
// build, ignoring the normal staleAfter cadence — see
// godl/internal/ytdlp.ForceUpdate, its counterpart for `godl update`.
func ForceUpdate(ctx context.Context, progress func(string)) (updated bool, err error) {
	dataDir, err := paths.DataDir()
	if err != nil {
		return false, err
	}
	binDir := filepath.Join(dataDir, "bin")
	ffmpegName, ffprobeName := binNames()
	if fi, err := os.Stat(filepath.Join(binDir, ffmpegName)); err != nil || fi.IsDir() {
		return false, fmt.Errorf("no managed ffmpeg installed yet (run a social download first to install one)")
	}
	os.WriteFile(checkedFile(binDir), nil, 0o644)
	return checkAndUpdate(ctx, binDir, ffmpegName, ffprobeName, progress)
}

func report(progress func(string), msg string) {
	if progress != nil {
		progress(msg)
	}
}

// extractTarXz streams a .tar.xz, pulling out entries whose base name is
// a key in want and writing them to binDir under the mapped value.
func extractTarXz(r io.Reader, binDir string, want map[string]string) error {
	xr, err := xz.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(xr)
	remaining := len(want)
	for remaining > 0 {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		base := filepath.Base(hdr.Name)
		destName, ok := want[base]
		if !ok {
			continue
		}
		if err := writeExecutable(filepath.Join(binDir, destName), tr); err != nil {
			return err
		}
		delete(want, base)
		remaining--
	}
	if remaining > 0 {
		return fmt.Errorf("archive missing expected file(s): %v", keys(want))
	}
	return nil
}

func extractZip(archivePath, binDir string, want map[string]string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		base := filepath.Base(f.Name)
		destName, ok := want[base]
		if !ok {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeExecutable(filepath.Join(binDir, destName), rc)
		rc.Close()
		if err != nil {
			return err
		}
		delete(want, base)
	}
	if len(want) > 0 {
		return fmt.Errorf("archive missing expected file(s): %v", keys(want))
	}
	return nil
}

func keys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func writeExecutable(dest string, r io.Reader) error {
	tmp := dest + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}
