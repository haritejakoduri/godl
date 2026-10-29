package appwindow

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestAppArgs pins the one flag the whole package rests on. --app is
// what drops the tab strip and address bar; --app URL (a space) opens
// an ordinary tab instead, which would look like the feature silently
// not working.
func TestAppArgs(t *testing.T) {
	got := appArgs("http://127.0.0.1:42069/?token=abc")
	want := []string{"--app=http://127.0.0.1:42069/?token=abc"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("appArgs() = %q, want %q", got, want)
	}
}

// TestBrowserCandidatesPerPlatform checks each platform's list is
// shaped the way findBrowser expects to consume it: PATH names on
// Linux, absolute paths on macOS and Windows, since a bare name there
// would never be found and an absolute path on Linux would skip the
// PATH lookup.
func TestBrowserCandidatesPerPlatform(t *testing.T) {
	t.Run("linux names are relative, for PATH", func(t *testing.T) {
		got := browserCandidates("linux")
		if len(got) == 0 {
			t.Fatal("no candidates for linux")
		}
		for _, c := range got {
			if filepath.IsAbs(c) {
				t.Errorf("candidate %q is absolute; findBrowser would stat it instead of searching PATH", c)
			}
		}
		if !contains(got, "google-chrome") || !contains(got, "chromium") {
			t.Errorf("linux candidates %q, want at least google-chrome and chromium", got)
		}
	})

	t.Run("darwin paths are absolute bundle executables", func(t *testing.T) {
		got := browserCandidates("darwin")
		if len(got) == 0 {
			t.Fatal("no candidates for darwin")
		}
		for _, c := range got {
			if !filepath.IsAbs(c) {
				t.Errorf("candidate %q is not absolute; macOS browsers are not on PATH", c)
			}
			// The bundle directory is not executable — the binary
			// inside Contents/MacOS is what can be started.
			if !strings.Contains(c, "/Contents/MacOS/") {
				t.Errorf("candidate %q does not point inside the bundle; starting a .app directory fails", c)
			}
		}
		if !hasPrefixIn(got, "/Applications/") {
			t.Errorf("darwin candidates %q, want /Applications looked at", got)
		}
	})

	t.Run("windows paths are built from the install roots", func(t *testing.T) {
		t.Setenv("ProgramFiles", `C:\Program Files`)
		t.Setenv("ProgramFiles(x86)", `C:\Program Files (x86)`)
		t.Setenv("LocalAppData", `C:\Users\u\AppData\Local`)

		got := browserCandidates("windows")
		for _, want := range []string{
			`C:\Program Files/Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)/Google\Chrome\Application\chrome.exe`,
			`C:\Users\u\AppData\Local/Google\Chrome\Application\chrome.exe`,
		} {
			if !contains(got, want) {
				t.Errorf("windows candidates missing %q\ngot: %q", want, got)
			}
		}
		// Edge matters more than the others here: it is on every
		// Windows 10 and 11 whether the user chose a browser or not,
		// so it is what makes the window work out of the box.
		if !hasSubstringIn(got, "msedge.exe") {
			t.Errorf("windows candidates %q, want Edge among them", got)
		}
	})

	// A root that isn't set must drop out rather than produce paths
	// rooted at the filesystem root, which could match something
	// unrelated.
	t.Run("windows skips roots the environment does not set", func(t *testing.T) {
		t.Setenv("ProgramFiles", `C:\Program Files`)
		t.Setenv("ProgramFiles(x86)", "")
		t.Setenv("LocalAppData", "")

		for _, c := range browserCandidates("windows") {
			if !strings.HasPrefix(c, `C:\Program Files`) {
				t.Errorf("candidate %q came from an unset root", c)
			}
		}
	})

	// An unknown GOOS gets the unix list rather than nothing, so a
	// *BSD build still finds a browser.
	t.Run("an unknown platform falls back to the unix names", func(t *testing.T) {
		if got := browserCandidates("freebsd"); len(got) == 0 {
			t.Error("no candidates for freebsd; want the unix PATH names")
		}
	})
}

// TestFindBrowserRejectsADirectory guards the macOS case specifically:
// "/Applications/Google Chrome.app" exists as a directory, and handing
// a directory to exec would fail at launch with nothing explaining it.
func TestFindBrowserRejectsADirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the candidate list and PATH semantics differ")
	}
	// findBrowser consults runtime.GOOS, so this exercises the real
	// list; what matters is that whatever it returns is startable.
	if exe, ok := findBrowser(); ok {
		if st, err := statFile(exe); err != nil || st.IsDir() {
			t.Fatalf("findBrowser() = %q, which is not a startable file", exe)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func hasPrefixIn(haystack []string, prefix string) bool {
	for _, h := range haystack {
		if strings.HasPrefix(h, prefix) {
			return true
		}
	}
	return false
}

func hasSubstringIn(haystack []string, sub string) bool {
	for _, h := range haystack {
		if strings.Contains(h, sub) {
			return true
		}
	}
	return false
}
