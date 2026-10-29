package ffmpeg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureUsesAnInstalledFFmpegWhereNoStaticBuildExists covers most
// machines, not an edge case: the static builds godl manages are
// x86-64 Linux and Windows only, so every Mac and every ARM machine —
// a Raspberry Pi, an ARM server, a phone — had nothing to fall back
// on and the video features just did not work there.
func TestEnsureUsesAnInstalledFFmpegWhereNoStaticBuildExists(t *testing.T) {
	t.Setenv("GODL_DATA_DIR", t.TempDir())

	restoreAsset, restoreLook := assetFor, lookPath
	defer func() { assetFor, lookPath = restoreAsset, restoreLook }()

	assetFor = func() (asset, error) { return asset{}, errors.New("no prebuilt ffmpeg for darwin/arm64") }
	lookPath = func(file string) (string, error) { return "/opt/homebrew/bin/" + file, nil }

	got, err := Ensure(context.Background(), nil)
	if err != nil {
		t.Fatalf("Ensure() = %v, want the installed ffmpeg's directory", err)
	}
	if want := "/opt/homebrew/bin"; got != want {
		t.Errorf("Ensure() = %q, want %q", got, want)
	}
}

// ffprobe is required, not optional: yt-dlp inspects streams with it
// partway through a merge, so an ffmpeg without one beside it fails
// late and confusingly instead of here.
func TestSystemDirNeedsFFprobeToo(t *testing.T) {
	restore := lookPath
	defer func() { lookPath = restore }()

	ffmpegName, ffprobeName := binNames()
	lookPath = func(file string) (string, error) {
		if file == ffprobeName {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + file, nil
	}
	if dir, ok := systemDir(); ok {
		t.Errorf("systemDir() = (%q, true) with no ffprobe; want it to decline", dir)
	}

	lookPath = func(file string) (string, error) { return "/usr/bin/" + file, nil }
	dir, ok := systemDir()
	if !ok || dir != "/usr/bin" {
		t.Errorf("systemDir() = (%q, %v) with both present, want (\"/usr/bin\", true)", dir, ok)
	}
	_ = ffmpegName
}

// With neither, the error says so — both halves, since installing one
// is the only way forward.
func TestEnsureReportsBothMissing(t *testing.T) {
	t.Setenv("GODL_DATA_DIR", t.TempDir())

	restoreAsset, restoreLook := assetFor, lookPath
	defer func() { assetFor, lookPath = restoreAsset, restoreLook }()

	assetFor = func() (asset, error) { return asset{}, errors.New("no prebuilt ffmpeg for darwin/arm64") }
	lookPath = func(string) (string, error) { return "", errors.New("not found") }

	_, err := Ensure(context.Background(), nil)
	if err == nil {
		t.Fatal("Ensure() succeeded with no build and nothing on PATH")
	}
	for _, want := range []string{"darwin/arm64", "none on PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// The managed copy still wins where there is one, so x86-64 Linux and
// Windows keep the behaviour the package doc describes.
func TestEnsurePrefersTheManagedCopy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GODL_DATA_DIR", dir)
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ffmpegName, _ := binNames()
	if err := os.WriteFile(filepath.Join(binDir, ffmpegName), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// checkedFile, not versionFile: that is the staleness stamp, and
	// without it maybeUpdate reaches for the network — which makes
	// this test slow, and dependent on something it is not testing.
	if err := os.WriteFile(checkedFile(binDir), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(versionFile(binDir), []byte("deadbeef"), 0o644); err != nil {
		t.Fatal(err)
	}

	restore := lookPath
	defer func() { lookPath = restore }()
	lookPath = func(string) (string, error) {
		t.Error("PATH was consulted even though godl has its own copy")
		return "", errors.New("unused")
	}

	got, err := Ensure(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got != binDir {
		t.Errorf("Ensure() = %q, want the managed directory %q", got, binDir)
	}
}
