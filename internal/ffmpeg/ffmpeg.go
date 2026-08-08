// Package ffmpeg locates ffmpeg/ffprobe for yt-dlp to use when merging
// separately-downloaded video and audio streams. If neither is on PATH,
// it auto-downloads a static build from BtbN/FFmpeg-Builds — a
// long-running community source of prebuilt ffmpeg binaries (ffmpeg.org
// itself doesn't publish simple direct-download static binaries) widely
// used by other CLI tools for exactly this purpose — the first time
// they're actually needed, and caches them under godl's data dir.
package ffmpeg

import (
	"archive/tar"
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/ulikunitz/xz"

	"godl/internal/ghrelease"
	"godl/internal/paths"
)

const releaseBase = "https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/"
const releaseAPI = "https://api.github.com/repos/BtbN/FFmpeg-Builds/releases/latest"

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

// Ensure returns a directory containing working ffmpeg and ffprobe
// binaries — wherever they already are on PATH, a previously
// auto-downloaded copy, or a freshly downloaded one. progress, if
// non-nil, is called with human-readable status lines while a download
// is in flight.
func Ensure(ctx context.Context, progress func(string)) (dir string, err error) {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return filepath.Dir(p), nil
	}

	dataDir, err := paths.DataDir()
	if err != nil {
		return "", err
	}
	binDir := filepath.Join(dataDir, "bin")
	ffmpegName, ffprobeName := binNames()

	if fi, err := os.Stat(filepath.Join(binDir, ffmpegName)); err == nil && !fi.IsDir() {
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

	url := releaseBase + a.file
	report(progress, "ffmpeg not found; downloading a static build from "+url+" (one-time download, a couple hundred MB)")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading ffmpeg: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading ffmpeg: unexpected status: %s", resp.Status)
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
		return "", err
	}
	tmpArchivePath := tmpArchive.Name()
	defer os.Remove(tmpArchivePath)
	gotHex, _, err := ghrelease.HashingCopy(tmpArchive, resp.Body)
	if err != nil {
		tmpArchive.Close()
		return "", fmt.Errorf("downloading ffmpeg: %w", err)
	}
	if err := tmpArchive.Close(); err != nil {
		return "", err
	}
	if err := ghrelease.Verify(gotHex, wantHex); err != nil {
		return "", err
	}
	report(progress, "ffmpeg checksum verified")

	want := map[string]string{ffmpegName: ffmpegName, ffprobeName: ffprobeName}

	switch a.kind {
	case kindTarXz:
		f, err := os.Open(tmpArchivePath)
		if err != nil {
			return "", err
		}
		defer f.Close()
		if err := extractTarXz(f, binDir, want); err != nil {
			return "", fmt.Errorf("extracting ffmpeg: %w", err)
		}
	case kindZip:
		if err := extractZip(tmpArchivePath, binDir, want); err != nil {
			return "", fmt.Errorf("extracting ffmpeg: %w", err)
		}
	}

	report(progress, "ffmpeg installed to "+binDir)
	return binDir, nil
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
