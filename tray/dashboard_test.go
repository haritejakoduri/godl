package tray

import (
	"strings"
	"testing"
)

// TestTerminalAppleScriptQuotesAwkwardPaths covers both quoting layers
// at once for macOS: a path holding a single quote (shell) and one
// holding a double quote or backslash (AppleScript). Neither is exotic
// — a home directory named O'Brien is enough — and getting it wrong
// silently opens a terminal that runs the wrong command, or none.
func TestTerminalAppleScriptQuotesAwkwardPaths(t *testing.T) {
	got := terminalAppleScript(`/Users/o'brien/My Apps/godl`)
	// Doubly escaped on purpose: the shell layer produces '\'' and the
	// AppleScript layer then escapes that backslash, so the literal
	// carries '\\''. AppleScript unescapes it back to '\'' for the
	// shell, which yields the bare path.
	if !strings.Contains(got, `'/Users/o'\\''brien/My Apps/godl' status`) {
		t.Errorf("single quote not escaped through both layers:\n%s", got)
	}

	got = terminalAppleScript(`/tmp/a"b\c/godl`)
	// Inside the AppleScript literal both characters must arrive
	// escaped, or the literal ends early and the script won't compile.
	if !strings.Contains(got, `a\"b`) || !strings.Contains(got, `\\c`) {
		t.Errorf("quote/backslash not escaped for AppleScript:\n%s", got)
	}
	if strings.Count(got, `do script "`) != 1 {
		t.Errorf("expected exactly one do script literal:\n%s", got)
	}
}

func TestShellQuoteWrapsAndEscapes(t *testing.T) {
	if got, want := shellQuote(`/usr/bin/godl`), `'/usr/bin/godl'`; got != want {
		t.Errorf("shellQuote = %q, want %q", got, want)
	}
	if got, want := shellQuote(`it's`), `'it'\''s'`; got != want {
		t.Errorf("shellQuote = %q, want %q", got, want)
	}
}
