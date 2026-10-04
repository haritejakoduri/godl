// Package jsruntime gives yt-dlp a JavaScript runtime.
//
// YouTube now hides its streams behind a small script ("n challenge")
// the client has to run. yt-dlp can run it, but only with a JavaScript
// runtime to hand; without one it gets no video formats at all, or URLs
// the site serves at a crawl. yt-dlp looks for deno by default and
// ignores everything else unless told.
//
// So godl tells it: a deno, node or bun already installed on PATH is
// used as it is; failing that, godl installs its own copy of deno from
// deno's GitHub releases, verified against the digest GitHub computed —
// the same way internal/ffmpeg and internal/ytdlp manage theirs.
package jsruntime

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"

	"godl/internal/ghrelease"
	"godl/internal/httpx"
	"godl/internal/paths"
)

const (
	releaseAPI  = "https://api.github.com/repos/denoland/deno/releases/latest"
	releaseBase = "https://github.com/denoland/deno/releases/latest/download/"
)

var httpClient = httpx.Client(httpx.BinaryFetchTimeout)

// lookPath is exec.LookPath, swappable in tests.
var lookPath = exec.LookPath

var mu sync.Mutex // one install at a time

// Args returns the yt-dlp flags that hand it a JavaScript runtime, or
// nil if none could be found or installed — yt-dlp then runs as it did
// before, which still works for most sites other than YouTube. progress,
// if non-nil, reports a first-time install.
func Args(ctx context.Context, progress func(string)) []string {
	name, path, err := Find(ctx, progress)
	if err != nil {
		if progress != nil {
			progress("warning: no JavaScript runtime for yt-dlp (" + err.Error() + ") — YouTube may offer fewer formats or slow downloads")
		}
		return nil
	}
	return []string{"--js-runtimes", name + ":" + path}
}

// Find returns a runtime yt-dlp supports: one on PATH, else godl's own
// deno, installing it the first time it's needed.
func Find(ctx context.Context, progress func(string)) (name, path string, err error) {
	for _, n := range []string{"deno", "node", "bun"} {
		if p, err := lookPath(n); err == nil {
			return n, p, nil
		}
	}
	mu.Lock()
	defer mu.Unlock()
	dir, err := binDir()
	if err != nil {
		return "", "", err
	}
	dest := filepath.Join(dir, exeName())
	if fi, err := os.Stat(dest); err == nil && !fi.IsDir() {
		return "deno", dest, nil
	}
	if err := install(ctx, dir, progress); err != nil {
		return "", "", err
	}
	return "deno", dest, nil
}

func binDir() (string, error) {
	data, err := paths.DataDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(data, "bin")
	return dir, os.MkdirAll(dir, 0o755)
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "deno.exe"
	}
	return "deno"
}

// assetName is deno's release zip for this platform.
func assetName() (string, error) {
	targets := map[string]string{
		"linux/amd64":   "x86_64-unknown-linux-gnu",
		"linux/arm64":   "aarch64-unknown-linux-gnu",
		"darwin/amd64":  "x86_64-apple-darwin",
		"darwin/arm64":  "aarch64-apple-darwin",
		"windows/amd64": "x86_64-pc-windows-msvc",
		"windows/arm64": "aarch64-pc-windows-msvc",
	}
	t, ok := targets[runtime.GOOS+"/"+runtime.GOARCH]
	if !ok {
		return "", fmt.Errorf("no deno build for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	return "deno-" + t + ".zip", nil
}

func install(ctx context.Context, dir string, progress func(string)) error {
	asset, err := assetName()
	if err != nil {
		return err
	}
	report(progress, "fetching deno release checksum...")
	wantHex, err := ghrelease.AssetDigest(ctx, releaseAPI, asset)
	if err != nil {
		return fmt.Errorf("looking up deno's published checksum (refusing to install unverified): %w", err)
	}
	report(progress, "downloading deno (a JavaScript runtime yt-dlp needs for YouTube; one-time, about 40 MB)...")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseBase+asset, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading deno: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading deno: %s", resp.Status)
	}
	tmp, err := os.CreateTemp(dir, "deno-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	gotHex, _, err := ghrelease.HashingCopy(tmp, resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("downloading deno: %w", err)
	}
	if err := ghrelease.Verify(gotHex, wantHex); err != nil {
		return err
	}
	if err := extract(tmp.Name(), filepath.Join(dir, exeName())); err != nil {
		return fmt.Errorf("unpacking deno: %w", err)
	}
	report(progress, "deno installed to "+dir)
	return nil
}

func extract(zipPath, dest string) error {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if filepath.Base(f.Name) != exeName() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()
		part := dest + ".part"
		out, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			os.Remove(part)
			return err
		}
		if err := out.Close(); err != nil {
			os.Remove(part)
			return err
		}
		return os.Rename(part, dest)
	}
	return fmt.Errorf("%s not found in the archive", exeName())
}

func report(progress func(string), msg string) {
	if progress != nil {
		progress(msg)
	}
}
