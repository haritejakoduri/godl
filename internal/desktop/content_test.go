package desktop

import (
	"encoding/xml"
	"strings"
	"testing"
)

// These cover the text the Windows and macOS entries are built from.
// Neither platform can be exercised here, and the failure mode is
// quiet — a shortcut that launches nothing, or a bundle macOS refuses —
// so the strings themselves are the thing worth pinning down.

// awkward is an ordinary path that breaks naive quoting: a space, and
// an apostrophe from a surname. Both are common in real home
// directories.
const awkward = `/Users/o'brien/My Apps/godl`

func TestLauncherScriptQuotesTheBinaryPath(t *testing.T) {
	got := launcherScript(awkward)

	if !strings.HasPrefix(got, "#!/bin/sh\n") {
		t.Errorf("launcher has no shebang, so macOS won't execute it:\n%s", got)
	}
	// '\'' is how a single quote is escaped inside a single-quoted
	// shell word: close, escaped quote, reopen.
	if !strings.Contains(got, `'/Users/o'\''brien/My Apps/godl' web`) {
		t.Errorf("binary path not shell-quoted:\n%s", got)
	}
	if !strings.Contains(got, "exec ") {
		t.Error("launcher should exec rather than fork, so the bundle's process is godl itself")
	}
}

func TestShellQuote(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`/usr/bin/godl`, `'/usr/bin/godl'`},
		{`/My Apps/godl`, `'/My Apps/godl'`},
		{`it's`, `'it'\''s'`},
	} {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestInfoPlistIsWellFormedXML: macOS silently refuses a bundle whose
// Info.plist doesn't parse, which looks like the app simply not being
// there. Parsing it is cheap insurance against a stray character in the
// template.
func TestInfoPlistIsWellFormedXML(t *testing.T) {
	got := infoPlistFor("1.2.3")

	if err := xml.Unmarshal([]byte(got), new(struct{})); err != nil {
		t.Fatalf("Info.plist is not well-formed XML: %v\n%s", err, got)
	}
	for _, want := range []string{
		"<key>CFBundleExecutable</key><string>godl</string>",
		"<key>CFBundleIconFile</key><string>godl</string>",
		"<key>CFBundlePackageType</key><string>APPL</string>",
		"<string>" + bundleID + "</string>",
		"<string>1.2.3</string>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Info.plist missing %q", want)
		}
	}
	// A background-only app is not reliably listed in Launchpad, which
	// is the one thing this bundle exists to achieve.
	if strings.Contains(got, "LSUIElement") {
		t.Error("Info.plist sets LSUIElement, which can hide the app from Launchpad")
	}
}

func TestInfoPlistCarriesTheVersionInBothKeys(t *testing.T) {
	got := infoPlistFor("9.9.9")
	if n := strings.Count(got, "<string>9.9.9</string>"); n != 2 {
		t.Errorf("version appears %d times, want 2 (CFBundleVersion and CFBundleShortVersionString)", n)
	}
}

func TestShortcutScriptQuotesPathsForPowerShell(t *testing.T) {
	link := `C:\Users\O'Brien\Start Menu\godl.lnk`
	exe := `C:\Program Files\o'brien\godl.exe`
	got := shortcutScript(link, exe, `C:\Program Files\o'brien`)

	// PowerShell escapes a single quote by doubling it inside a
	// single-quoted string. Leaving it bare would end the string early
	// and produce a parse error rather than a shortcut.
	if !strings.Contains(got, `'C:\Users\O''Brien\Start Menu\godl.lnk'`) {
		t.Errorf("link path not PowerShell-quoted:\n%s", got)
	}
	if !strings.Contains(got, `'C:\Program Files\o''brien\godl.exe'`) {
		t.Errorf("exe path not PowerShell-quoted:\n%s", got)
	}
	// Every quote in the script must be balanced, or PowerShell rejects
	// the whole command.
	if strings.Count(got, "'")%2 != 0 {
		t.Errorf("odd number of quotes — the script won't parse:\n%s", got)
	}
	for _, want := range []string{"CreateShortcut(", "$s.TargetPath", "$s.Arguments = 'web'", "$s.IconLocation", "$s.Save()"} {
		if !strings.Contains(got, want) {
			t.Errorf("shortcut script missing %q", want)
		}
	}
}

func TestPSQuote(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{`C:\godl.exe`, `'C:\godl.exe'`},
		{`C:\it's\godl.exe`, `'C:\it''s\godl.exe'`},
	} {
		if got := psQuote(tc.in); got != tc.want {
			t.Errorf("psQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestShortcutTargetsTheBinaryNotTheScript guards the installer's use
// of InstallFor: the shortcut must point at godl.exe, never at
// godl-setup.exe, or launching it would re-run the installer.
func TestShortcutTargetsTheGivenExe(t *testing.T) {
	got := shortcutScript(`C:\sm\godl.lnk`, `C:\apps\godl.exe`, `C:\apps`)
	if !strings.Contains(got, `$s.TargetPath = 'C:\apps\godl.exe'`) {
		t.Errorf("TargetPath is not the exe it was given:\n%s", got)
	}
}
