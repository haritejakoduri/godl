package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
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

// fakePackageInstall makes ForceUpdate believe it's running as the
// package-installed /usr/bin/godl, owned by the .rpm (or not, per
// isRPM), and swaps the real package manager for install. Nothing is
// ever written to that path: the rpm branch only downloads into a temp
// dir and calls installRPM.
func fakePackageInstall(t *testing.T, isRPM bool, install func(rpmPath string) error) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("package-managed installs only exist on Linux")
	}
	origExe, origOwned, origInstall := osExecutable, rpmOwned, installRPM
	osExecutable = func() (string, error) { return packageManagedPath, nil }
	rpmOwned = func(context.Context, string) bool { return isRPM }
	installRPM = func(_ context.Context, rpmPath string) error { return install(rpmPath) }
	t.Cleanup(func() { osExecutable, rpmOwned, installRPM = origExe, origOwned, origInstall })
}

func TestForceUpdateRPMDownloadsVerifiesAndInstalls(t *testing.T) {
	assetFileName, ok := rpmAssetName("9.9.9")
	if !ok {
		t.Skip("no published .rpm for this architecture")
	}
	rpmContent := []byte("fake-rpm-contents-v9.9.9\n")
	fakeRelease(t, "v9.9.9", assetFileName, rpmContent)

	var installedPath string
	var installed []byte
	fakePackageInstall(t, true, func(rpmPath string) error {
		installedPath = rpmPath
		var err error
		installed, err = os.ReadFile(rpmPath)
		return err
	})

	result, latestVersion, err := ForceUpdate(context.Background(), nil)
	if err != nil {
		t.Fatalf("ForceUpdate error: %v", err)
	}
	if result != Updated || latestVersion != "9.9.9" {
		t.Fatalf("ForceUpdate = (%v, %q), want (Updated, \"9.9.9\")", result, latestVersion)
	}
	if filepath.Base(installedPath) != assetFileName {
		t.Fatalf("installRPM got %q, want a file named %q", installedPath, assetFileName)
	}
	if string(installed) != string(rpmContent) {
		t.Fatalf("installRPM saw %q, want the downloaded release asset %q", installed, rpmContent)
	}
	if _, err := os.Stat(installedPath); !os.IsNotExist(err) {
		t.Fatalf("downloaded rpm %s still exists after the update (stat err: %v)", installedPath, err)
	}
}

func TestForceUpdateRPMRejectsChecksumMismatch(t *testing.T) {
	assetFileName, ok := rpmAssetName("9.9.9")
	if !ok {
		t.Skip("no published .rpm for this architecture")
	}
	fakeRelease(t, "v9.9.9", assetFileName, []byte("legit content"))
	mux := http.NewServeMux()
	mux.HandleFunc("/tampered/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("tampered content"))
	})
	tamperSrv := httptest.NewServer(mux)
	defer tamperSrv.Close()
	releaseBase = func(string) string { return tamperSrv.URL + "/tampered/" }

	fakePackageInstall(t, true, func(rpmPath string) error {
		t.Errorf("installRPM called with %s; a package that failed verification must never reach the package manager", rpmPath)
		return nil
	})

	if result, _, err := ForceUpdate(context.Background(), nil); err == nil {
		t.Fatalf("ForceUpdate result = %v, err = nil; want a checksum-mismatch error", result)
	}
}

// A failed install (wrong sudo password, dnf refusing the transaction)
// has to surface as an error, not as a silent "updated".
func TestForceUpdateRPMInstallFailure(t *testing.T) {
	assetFileName, ok := rpmAssetName("9.9.9")
	if !ok {
		t.Skip("no published .rpm for this architecture")
	}
	fakeRelease(t, "v9.9.9", assetFileName, []byte("fake-rpm"))
	fakePackageInstall(t, true, func(string) error { return errors.New("sudo: 3 incorrect password attempts") })

	result, latestVersion, err := ForceUpdate(context.Background(), nil)
	if err == nil || result == Updated {
		t.Fatalf("ForceUpdate = (%v, err %v); want an error and no Updated result", result, err)
	}
	if latestVersion != "9.9.9" {
		t.Fatalf("ForceUpdate latestVersion = %q, want %q", latestVersion, "9.9.9")
	}
}

func TestForceUpdateRPMAlreadyLatest(t *testing.T) {
	fakeRelease(t, "v"+version.Version, "unused", nil)
	fakePackageInstall(t, true, func(rpmPath string) error {
		t.Errorf("installRPM called with %s on an already-current install", rpmPath)
		return nil
	})

	result, _, err := ForceUpdate(context.Background(), nil)
	if err != nil || result != AlreadyLatest {
		t.Fatalf("ForceUpdate = (%v, err %v), want (AlreadyLatest, nil)", result, err)
	}
}

// A .deb install shares /usr/bin/godl with the .rpm one but isn't in
// any rpm database: still left entirely to apt, without so much as a
// release lookup.
func TestForceUpdateDebInstallLeftToApt(t *testing.T) {
	fakeRelease(t, "v9.9.9", "unused", nil)
	fakePackageInstall(t, false, func(rpmPath string) error {
		t.Errorf("installRPM called with %s on a non-rpm install", rpmPath)
		return nil
	})

	result, latestVersion, err := ForceUpdate(context.Background(), nil)
	if err != nil || result != ManagedInstall || latestVersion != "" {
		t.Fatalf("ForceUpdate = (%v, %q, err %v), want (ManagedInstall, \"\", nil)", result, latestVersion, err)
	}
}
