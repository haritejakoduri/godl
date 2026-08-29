package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"godl/internal/version"
)

// fakeRelease spins up an httptest server shaped like the two GitHub
// API responses ForceUpdate needs (the release object, and the asset
// download itself) and points the package's release-location vars at
// it for the duration of the test.
func fakeRelease(t *testing.T, tag, assetFileName string, assetContent []byte) *httptest.Server {
	t.Helper()
	sum := sha256.Sum256(assetContent)
	digestHex := hex.EncodeToString(sum[:])

	mux := http.NewServeMux()
	mux.HandleFunc("/release", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"assets":[{"name":%q,"digest":"sha256:%s"}]}`, tag, assetFileName, digestHex)
	})
	mux.HandleFunc("/download/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(assetContent)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	origAPI, origBase := releaseAPI, releaseBase
	releaseAPI = srv.URL + "/release"
	releaseBase = func(string) string { return srv.URL + "/download/" }
	t.Cleanup(func() { releaseAPI, releaseBase = origAPI, origBase })

	return srv
}

// fakeExecutable points osExecutable (what ForceUpdate calls instead of
// os.Executable directly) at a real file under t.TempDir with the given
// initial content, for the duration of the test.
func fakeExecutable(t *testing.T, initial []byte) string {
	t.Helper()
	exePath := filepath.Join(t.TempDir(), "godl")
	if err := os.WriteFile(exePath, initial, 0o755); err != nil {
		t.Fatal(err)
	}
	orig := osExecutable
	osExecutable = func() (string, error) { return exePath, nil }
	t.Cleanup(func() { osExecutable = orig })
	return exePath
}

func TestForceUpdateDownloadsVerifiesAndReplaces(t *testing.T) {
	assetFileName, ok := assetName("9.9.9")
	if !ok {
		t.Skip("no published raw binary asset for this platform — nothing to smoke test here")
	}
	newContent := []byte("fake-new-binary-contents-v9.9.9\n")
	fakeRelease(t, "v9.9.9", assetFileName, newContent)
	exePath := fakeExecutable(t, []byte("old-binary-contents\n"))

	var lines []string
	result, latestVersion, err := ForceUpdate(context.Background(), func(msg string) { lines = append(lines, msg) })
	if err != nil {
		t.Fatalf("ForceUpdate error: %v", err)
	}
	if result != Updated {
		t.Fatalf("ForceUpdate result = %v, want Updated (progress: %v)", result, lines)
	}
	if latestVersion != "9.9.9" {
		t.Fatalf("ForceUpdate latestVersion = %q, want %q", latestVersion, "9.9.9")
	}

	got, err := os.ReadFile(exePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newContent) {
		t.Fatalf("binary at exePath = %q, want %q", got, newContent)
	}
	fi, err := os.Stat(exePath)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Fatalf("replaced binary lost its executable bit: mode=%v", fi.Mode())
	}
}

func TestForceUpdateAlreadyLatest(t *testing.T) {
	// No asset needed: ForceUpdate returns AlreadyLatest as soon as the
	// tag matches the running version, before ever looking up an asset.
	fakeRelease(t, "v"+version.Version, "unused", nil)
	fakeExecutable(t, []byte("current-binary\n"))

	result, latestVersion, err := ForceUpdate(context.Background(), nil)
	if err != nil {
		t.Fatalf("ForceUpdate error: %v", err)
	}
	if result != AlreadyLatest {
		t.Fatalf("ForceUpdate result = %v, want AlreadyLatest", result)
	}
	if latestVersion != version.Version {
		t.Fatalf("ForceUpdate latestVersion = %q, want the running version %q", latestVersion, version.Version)
	}
}

func TestForceUpdateRejectsChecksumMismatch(t *testing.T) {
	assetFileName, ok := assetName("9.9.9")
	if !ok {
		t.Skip("no published raw binary asset for this platform — nothing to smoke test here")
	}
	// fakeRelease publishes a digest computed over "legit content", but
	// the download handler below actually serves different bytes —
	// simulating corruption in transit or a compromised/misconfigured
	// CDN edge, the exact threat ghrelease.Verify exists to catch.
	fakeRelease(t, "v9.9.9", assetFileName, []byte("legit content"))
	mux := http.NewServeMux()
	mux.HandleFunc("/tampered/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("tampered content"))
	})
	tamperSrv := httptest.NewServer(mux)
	defer tamperSrv.Close()
	releaseBase = func(string) string { return tamperSrv.URL + "/tampered/" }

	original := []byte("old-binary-contents\n")
	exePath := fakeExecutable(t, original)

	result, _, err := ForceUpdate(context.Background(), nil)
	if err == nil {
		t.Fatalf("ForceUpdate result = %v, err = nil; want a checksum-mismatch error", result)
	}

	got, rerr := os.ReadFile(exePath)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if string(got) != string(original) {
		t.Fatalf("binary at exePath = %q after a rejected update; want it untouched (%q)", got, original)
	}
}

// TestForceUpdateUnsupportedStillReportsLatestVersion is the
// regression test for cmd/update.go's "no self-update available"
// message: even on a platform with no published raw binary asset
// (Windows in practice, forced here via assetName so the test isn't
// tied to whatever platform actually runs it), ForceUpdate must still
// look up and return the latest published version — so the message
// shown can say what's actually out there instead of a bare "go check
// yourself".
func TestForceUpdateUnsupportedStillReportsLatestVersion(t *testing.T) {
	origAssetName := assetName
	assetName = func(string) (string, bool) { return "", false }
	t.Cleanup(func() { assetName = origAssetName })

	fakeRelease(t, "v9.9.9", "unused", nil)
	fakeExecutable(t, []byte("current-binary\n"))

	result, latestVersion, err := ForceUpdate(context.Background(), nil)
	if err != nil {
		t.Fatalf("ForceUpdate error: %v", err)
	}
	if result != Unsupported {
		t.Fatalf("ForceUpdate result = %v, want Unsupported", result)
	}
	if latestVersion != "9.9.9" {
		t.Fatalf("ForceUpdate latestVersion = %q, want %q (must still be looked up on an unsupported platform)", latestVersion, "9.9.9")
	}
}
