package mpv

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestJoinComma(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"Authorization: Basic abc"}, "Authorization: Basic abc"},
		{[]string{"A: 1", "B: 2"}, "A: 1,B: 2"},
	}
	for _, c := range cases {
		if got := joinComma(c.in); got != c.want {
			t.Errorf("joinComma(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestInstallHint(t *testing.T) {
	// Just confirm every platform gets a non-empty, actionable hint —
	// the exact wording is allowed to change.
	if h := installHint(); h == "" {
		t.Error("installHint() returned empty string")
	}
}

func TestVLCCandidatePaths(t *testing.T) {
	env := map[string]string{
		"ProgramFiles":      `C:\Program Files`,
		"ProgramFiles(x86)": `C:\Program Files (x86)`,
	}
	getenv := func(k string) string { return env[k] }

	got := vlcCandidatePaths("windows", getenv)
	want := []string{
		`C:\Program Files\VideoLAN\VLC\vlc.exe`,
		`C:\Program Files (x86)\VideoLAN\VLC\vlc.exe`,
	}
	if len(got) != len(want) {
		t.Fatalf("vlcCandidatePaths(windows, ...) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("vlcCandidatePaths(windows, ...)[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	if got := vlcCandidatePaths("linux", getenv); got != nil {
		t.Errorf("vlcCandidatePaths(linux, ...) = %v, want nil (Windows-only fallback)", got)
	}
	if got := vlcCandidatePaths("darwin", getenv); got != nil {
		t.Errorf("vlcCandidatePaths(darwin, ...) = %v, want nil (Windows-only fallback)", got)
	}

	// A missing env var is skipped rather than producing a bogus path
	// with an empty directory component.
	got = vlcCandidatePaths("windows", func(k string) string {
		if k == "ProgramFiles" {
			return `C:\Program Files`
		}
		return ""
	})
	if len(got) != 1 || got[0] != `C:\Program Files\VideoLAN\VLC\vlc.exe` {
		t.Errorf("vlcCandidatePaths with only ProgramFiles set = %v, want [C:\\Program Files\\VideoLAN\\VLC\\vlc.exe]", got)
	}
}

// TestPlayReportsNeitherPlayerFound points PATH at an empty directory
// so exec.LookPath fails for both mpv and vlc, reliably regardless of
// whether the machine actually running this test suite happens to
// have either installed — and since this test doesn't run on Windows,
// vlcCandidatePaths' fallback locations are never in play either, so
// findPlayer must come up empty and name both players in the error.
func TestPlayReportsNeitherPlayerFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	err := Play("/home/alice/Downloads/movie.mkv", nil)
	if err == nil {
		t.Fatal("Play with neither mpv nor vlc on PATH returned no error")
	}
	if !strings.Contains(err.Error(), "mpv") || !strings.Contains(err.Error(), "VLC") {
		t.Errorf("error %q doesn't mention both players — a user with neither installed should be told about both options", err.Error())
	}
}

// TestPlayRejectsHeadersWhenOnlyVLCAvailable confirms Play refuses a
// target that needs custom headers rather than silently dropping them
// (or, worse, falling back to embedding credentials in the URL) when
// mpv isn't available and only VLC is found — VLC has no equivalent to
// mpv's --http-header-fields flag. Skipped on Windows: the fake
// executable below is a POSIX shell script, not something Windows can
// exec directly.
func TestPlayRejectsHeadersWhenOnlyVLCAvailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake vlc executable below is a POSIX shell script")
	}

	dir := t.TempDir()
	fakeVLC := filepath.Join(dir, "vlc")
	if err := os.WriteFile(fakeVLC, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir) // only "vlc" is found; mpv is not

	err := Play("https://example.com/stream", map[string]string{"Authorization": "Basic abc"})
	if err == nil {
		t.Fatal("Play with headers and only VLC available succeeded, want an error")
	}
	if !strings.Contains(err.Error(), "VLC") {
		t.Errorf("error %q doesn't explain VLC is the reason, want it to mention VLC", err.Error())
	}
}
