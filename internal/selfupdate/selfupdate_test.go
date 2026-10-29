package selfupdate

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestAssetName(t *testing.T) {
	name, ok := assetName("0.3.0")
	switch runtime.GOOS + "/" + runtime.GOARCH {
	case "linux/amd64":
		if !ok || name != "godl-0.3.0-linux-amd64" {
			t.Errorf("assetName(0.3.0) = (%q, %v), want (\"godl-0.3.0-linux-amd64\", true)", name, ok)
		}
	case "darwin/arm64":
		if !ok || name != "godl-0.3.0-darwin-arm64" {
			t.Errorf("assetName(0.3.0) = (%q, %v), want (\"godl-0.3.0-darwin-arm64\", true)", name, ok)
		}
	default:
		if ok {
			t.Errorf("assetName(0.3.0) on unsupported platform %s/%s = (%q, true), want ok=false", runtime.GOOS, runtime.GOARCH, name)
		}
	}
}

// TestPackageManaged covers both package formats that install to
// /usr/bin/godl (.deb and .rpm) — the check can't tell them apart by
// path alone (rpmOwned does that), only that some package owns it and
// the binary must never be swapped directly.
func TestPackageManaged(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/usr/bin/godl", runtime.GOOS == "linux"},
		{"/usr/local/bin/godl", false},
		{"/home/alice/.local/bin/godl", false},
		{"", false},
	}
	for _, c := range cases {
		if got := packageManaged(c.path); got != c.want {
			t.Errorf("packageManaged(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestReleaseBase(t *testing.T) {
	got := releaseBase("v0.3.0")
	want := "https://github.com/haritejakoduri/godl/releases/download/v0.3.0/"
	if got != want {
		t.Errorf("releaseBase(v0.3.0) = %q, want %q", got, want)
	}
}

func TestRPMAssetName(t *testing.T) {
	name, ok := rpmAssetName("0.3.0")
	if runtime.GOARCH != "amd64" {
		if ok {
			t.Errorf("rpmAssetName(0.3.0) on %s = (%q, true), want ok=false", runtime.GOARCH, name)
		}
		return
	}
	if !ok || name != "godl-0.3.0-1.x86_64.rpm" {
		t.Errorf("rpmAssetName(0.3.0) = (%q, %v), want (\"godl-0.3.0-1.x86_64.rpm\", true)", name, ok)
	}
}

// readScript reads one of the build scripts, which these tests compare
// against rather than restating what they publish.
func readScript(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestRawAssetsMatchTheBuildScripts is the guard against the drift
// that made every Intel Mac un-updatable: build-darwin.sh began
// publishing darwin-amd64, rawAssets was never told, and the only
// symptom was godl quietly reporting no update available. Nothing
// fails when these two disagree, which is exactly why it needs a test.
//
// Windows is excluded deliberately, not forgotten: a windows-arm64
// binary is published, but Windows updates through its installer (see
// the package doc).
func TestRawAssetsMatchTheBuildScripts(t *testing.T) {
	published := map[string]string{}

	// build-all.sh calls "raw <goos> <goarch> <goarm> <suffix>".
	rawLine := regexp.MustCompile(`(?m)^raw\s+(\S+)\s+(\S+)\s+(\S+)\s+(\S+)\s*$`)
	for _, m := range rawLine.FindAllStringSubmatch(readScript(t, "build-all.sh"), -1) {
		goos, goarch, suffix := m[1], m[2], strings.Trim(m[4], `"`)
		if goos == "windows" {
			continue
		}
		published[goos+"/"+goarch] = suffix
	}
	// build-darwin.sh loops "for arch in <arches>" and writes
	// dist/godl-${VERSION}-darwin-${arch}.
	dar := regexp.MustCompile(`for arch in ([a-z0-9 ]+);`).FindStringSubmatch(readScript(t, "build-darwin.sh"))
	if dar == nil {
		t.Fatal("could not find the architecture loop in build-darwin.sh")
	}
	for _, a := range strings.Fields(dar[1]) {
		published["darwin/"+a] = "darwin-" + a
	}

	if len(published) == 0 {
		t.Fatal("parsed no platforms out of the build scripts; the parsing, not the lists, is wrong")
	}
	for platform, suffix := range published {
		got, ok := rawAssets[platform]
		if !ok {
			t.Errorf("%s is published by the build scripts but rawAssets has no entry, so godl there reports no update is available", platform)
			continue
		}
		if got != suffix {
			t.Errorf("%s: rawAssets says %q, the build scripts publish %q", platform, got, suffix)
		}
	}
	for platform := range rawAssets {
		if _, ok := published[platform]; !ok {
			t.Errorf("rawAssets has %s, which no build script publishes — godl there would download a 404", platform)
		}
	}
}

// TestRPMAssetNamesMatchTheBuildScript: the same drift, for the
// package-manager path. build-all.sh decides which architectures get
// an .rpm at all.
func TestRPMAssetNamesMatchTheBuildScript(t *testing.T) {
	all := readScript(t, "build-all.sh")
	want := map[string]bool{}
	for _, m := range regexp.MustCompile(`build-rpm\.sh"?\s+(\S+)`).FindAllStringSubmatch(all, -1) {
		want[strings.Trim(m[1], `"`)] = true
	}
	if len(want) == 0 {
		t.Fatal("found no build-rpm.sh invocations in build-all.sh")
	}
	got := map[string]bool{}
	for _, a := range rpmArches {
		got[a] = true
	}
	for a := range want {
		if !got[a] {
			t.Errorf("build-all.sh builds an %s .rpm but rpmArches has no Go architecture mapping to it", a)
		}
	}
	for a := range got {
		if !want[a] {
			t.Errorf("rpmArches maps to %s, which build-all.sh never builds — an update there would 404", a)
		}
	}
}
