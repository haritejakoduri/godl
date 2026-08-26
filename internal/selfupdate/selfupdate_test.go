package selfupdate

import (
	"runtime"
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

func TestDpkgManaged(t *testing.T) {
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
		if got := dpkgManaged(c.path); got != c.want {
			t.Errorf("dpkgManaged(%q) = %v, want %v", c.path, got, c.want)
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
