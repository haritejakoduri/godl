// Package appwindow opens godl's web interface in a window of its own
// instead of a tab in the user's browser: no tabs, no address bar, its
// own icon and its own entry in the dock or taskbar.
//
// There is no native window here, and that is deliberate. A real
// embedded webview — WKWebView, WebView2, WebKitGTK — would mean cgo on
// every platform rather than just macOS, a libwebkit2gtk build and
// runtime dependency for Linux, and the end of the single static binary
// anyone can download and run. That is a steep price for a window
// frame, so this borrows one instead: every Chromium-family browser
// takes --app=URL and gives back exactly the window wanted, and the
// desktop already has one.
//
// What it cannot do is conjure a window where there is no such browser.
// Safari has no equivalent and Firefox removed its site-specific
// browser mode, so a machine with only those falls back to a tab —
// which is what godl did everywhere until now.
package appwindow

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"godl/internal/guide"
)

// Open shows url in an app window, falling back to the user's default
// browser when there is no Chromium-family browser to ask. The error is
// only about failing to open anything at all: a tab instead of a window
// is a worse result, not a failure.
func Open(url string) error {
	if exe, ok := findBrowser(); ok {
		cmd := exec.Command(exe, appArgs(url)...)
		if err := cmd.Start(); err == nil {
			// Not waited on: the window outlives this command, and
			// godl has nothing to learn from when the user closes it.
			return cmd.Process.Release()
		}
		// A browser that is present but won't start is no reason to
		// give up — the default browser may be a different one.
	}
	return guide.OpenInBrowser(url)
}

// appArgs is the whole trick. Chromium's --app takes a URL and drops
// the tab strip, the address bar and the browser's own menus.
//
// No --window-size: Chromium remembers an app window's bounds per URL,
// so passing one would move the window back every launch and undo
// wherever the user put it.
func appArgs(url string) []string {
	return []string{"--app=" + url}
}

// statFile is os.Stat, named so the tests can assert on what
// findBrowser accepted without importing os for one call.
var statFile = os.Stat

// findBrowser returns the first candidate that exists.
func findBrowser() (string, bool) {
	for _, c := range browserCandidates(runtime.GOOS) {
		// Absolute paths are the macOS and Windows case, where
		// browsers install to known locations and are not on PATH.
		if filepath.IsAbs(c) {
			if st, err := statFile(c); err == nil && !st.IsDir() {
				return c, true
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p, true
		}
	}
	return "", false
}

// browserCandidates lists what to try, in order. Chrome first because
// it is the most likely to be the user's actual browser, then Edge
// (which every Windows 10 and 11 has whether the user chose it or not),
// then the rest.
//
// goos is a parameter rather than read from runtime so the list for
// each platform can be tested from any of them.
func browserCandidates(goos string) []string {
	switch goos {
	case "darwin":
		names := []string{
			"Google Chrome.app/Contents/MacOS/Google Chrome",
			"Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"Brave Browser.app/Contents/MacOS/Brave Browser",
			"Vivaldi.app/Contents/MacOS/Vivaldi",
			"Chromium.app/Contents/MacOS/Chromium",
		}
		// /Applications first, then the per-user one, matching how
		// macOS itself resolves an application.
		dirs := []string{"/Applications"}
		if home, err := os.UserHomeDir(); err == nil {
			dirs = append(dirs, filepath.Join(home, "Applications"))
		}
		var out []string
		for _, d := range dirs {
			for _, n := range names {
				out = append(out, filepath.Join(d, n))
			}
		}
		return out

	case "windows":
		rel := []string{
			`Google\Chrome\Application\chrome.exe`,
			`Microsoft\Edge\Application\msedge.exe`,
			`BraveSoftware\Brave-Browser\Application\brave.exe`,
			`Vivaldi\Application\vivaldi.exe`,
			`Chromium\Application\chrome.exe`,
		}
		// A browser can be installed for the machine or just for this
		// user, and on 64-bit Windows a 32-bit build lands in the
		// (x86) tree, so all three roots have to be looked at.
		var roots []string
		for _, v := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if d := os.Getenv(v); d != "" {
				roots = append(roots, d)
			}
		}
		var out []string
		for _, r := range roots {
			for _, n := range rel {
				out = append(out, filepath.Join(r, n))
			}
		}
		return out

	default:
		// Linux and the other unixes: these are on PATH, and which
		// name a distribution uses varies.
		return []string{
			"google-chrome",
			"google-chrome-stable",
			"chromium",
			"chromium-browser",
			"microsoft-edge",
			"microsoft-edge-stable",
			"brave-browser",
			"vivaldi-stable",
		}
	}
}

// Browser names the browser an app window would come from, and reports
// whether there is one at all. Callers use it to say which of the two
// results the user is about to get — "why did godl open in Chrome when
// Firefox is my default?" is otherwise a puzzle.
func Browser() (string, bool) {
	exe, ok := findBrowser()
	if !ok {
		return "", false
	}
	return filepath.Base(exe), true
}
