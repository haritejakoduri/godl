package ytdlp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsureUsesAnInstalledYtdlpWherePublishedBuildsDontExist is the
// Android and other-platform case. yt-dlp publishes standalone builds
// for Linux, macOS and Windows only; everywhere else godl has nothing
// to manage, and before this it simply refused — while reporting
// "not found on PATH" from code that never looked there.
func TestEnsureUsesAnInstalledYtdlpWherePublishedBuildsDontExist(t *testing.T) {
	t.Setenv("GODL_DATA_DIR", t.TempDir())

	restoreAsset, restoreLook := assetName, lookPath
	defer func() { assetName, lookPath = restoreAsset, restoreLook }()

	assetName = func() (string, error) { return "", errors.New("no prebuilt yt-dlp binary for android/arm64") }
	lookPath = func(file string) (string, error) {
		if file != "yt-dlp" {
			t.Errorf("looked for %q, want yt-dlp", file)
		}
		return "/data/data/com.termux/files/usr/bin/yt-dlp", nil
	}

	got, err := Ensure(context.Background(), nil)
	if err != nil {
		t.Fatalf("Ensure() = %v, want the installed yt-dlp", err)
	}
	if want := "/data/data/com.termux/files/usr/bin/yt-dlp"; got != want {
		t.Errorf("Ensure() = %q, want %q", got, want)
	}
}

// And when there is neither, the error has to say both things, since
// "install it yourself" is the only way out.
func TestEnsureReportsBothMissing(t *testing.T) {
	t.Setenv("GODL_DATA_DIR", t.TempDir())

	restoreAsset, restoreLook := assetName, lookPath
	defer func() { assetName, lookPath = restoreAsset, restoreLook }()

	assetName = func() (string, error) { return "", errors.New("no prebuilt yt-dlp binary for android/arm64") }
	lookPath = func(string) (string, error) { return "", errors.New("not found") }

	_, err := Ensure(context.Background(), nil)
	if err == nil {
		t.Fatal("Ensure() succeeded with no build and nothing on PATH")
	}
	for _, want := range []string{"android/arm64", "none on PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// A managed copy already on disk still wins, so the platforms that do
// have a published build keep behaving as the package doc describes —
// godl's own copy, kept current, PATH ignored.
func TestEnsurePrefersTheManagedCopy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GODL_DATA_DIR", dir)
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	managed := filepath.Join(binDir, localName())
	if err := os.WriteFile(managed, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Fresh enough that maybeUpdate does not reach for the network.
	if err := os.WriteFile(checkedFile(binDir), nil, 0o644); err != nil {
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
	if got != managed {
		t.Errorf("Ensure() = %q, want the managed copy %q", got, managed)
	}
}
