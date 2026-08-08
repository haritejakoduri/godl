// Package ytdlp locates a yt-dlp binary for "godl social" to shell out
// to, auto-downloading a standalone (no Python required) copy from
// yt-dlp's GitHub releases the first time it's needed if one isn't
// already on PATH.
package ytdlp

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"godl/internal/ghrelease"
	"godl/internal/paths"
)

const releaseBase = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/"
const releaseAPI = "https://api.github.com/repos/yt-dlp/yt-dlp/releases/latest"

// assetName picks the standalone, no-Python-required yt-dlp build for
// the current platform. See https://github.com/yt-dlp/yt-dlp/releases.
func assetName() (string, error) {
	switch runtime.GOOS {
	case "linux":
		if runtime.GOARCH == "arm64" {
			return "yt-dlp_linux_aarch64", nil
		}
		return "yt-dlp_linux", nil
	case "darwin":
		return "yt-dlp_macos", nil
	case "windows":
		return "yt-dlp.exe", nil
	default:
		return "", fmt.Errorf("no prebuilt yt-dlp binary for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
}

func localName() string {
	if runtime.GOOS == "windows" {
		return "yt-dlp.exe"
	}
	return "yt-dlp"
}

// Ensure returns a path to a working yt-dlp binary: one already on PATH
// if present, otherwise a copy godl downloaded on a previous run,
// otherwise a freshly downloaded one (saved under godl's data dir so
// it's reused next time). progress, if non-nil, is called with
// human-readable status lines while a download is in flight.
func Ensure(ctx context.Context, progress func(string)) (string, error) {
	if p, err := exec.LookPath("yt-dlp"); err == nil {
		return p, nil
	}

	dataDir, err := paths.DataDir()
	if err != nil {
		return "", err
	}
	binDir := filepath.Join(dataDir, "bin")
	localPath := filepath.Join(binDir, localName())

	if fi, err := os.Stat(localPath); err == nil && !fi.IsDir() {
		return localPath, nil
	}

	asset, err := assetName()
	if err != nil {
		return "", fmt.Errorf("yt-dlp not found on PATH, and godl can't auto-install it: %w (install it yourself: https://github.com/yt-dlp/yt-dlp#installation)", err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}

	report(progress, "fetching yt-dlp release checksum...")
	wantHex, err := ghrelease.AssetDigest(ctx, releaseAPI, asset)
	if err != nil {
		return "", fmt.Errorf("looking up yt-dlp's published checksum (refusing to download unverified): %w", err)
	}

	url := releaseBase + asset
	report(progress, "yt-dlp not found; downloading a standalone copy from "+url)
	if err := download(ctx, url, localPath, wantHex); err != nil {
		return "", fmt.Errorf("downloading yt-dlp: %w", err)
	}
	report(progress, "yt-dlp installed to "+localPath+" (checksum verified)")
	return localPath, nil
}

func report(progress func(string), msg string) {
	if progress != nil {
		progress(msg)
	}
}

// download fetches url to dest, verifying the downloaded bytes' sha256
// against wantHex before the file is renamed into place — a checksum
// mismatch (or any error) leaves no file at dest.
func download(ctx context.Context, url, dest, wantHex string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status: %s", resp.Status)
	}

	tmp := dest + ".part"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	gotHex, _, err := ghrelease.HashingCopy(f, resp.Body)
	if err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := ghrelease.Verify(gotHex, wantHex); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o755); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dest)
}
