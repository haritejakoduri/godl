package tray

import "testing"

// TestIsAquaSession covers the macOS session probe's string matching,
// which is the half that can be tested anywhere. launchctl's output is
// a bare name with a trailing newline, and the names that are not Aqua
// are the whole point: those sessions are real logins that simply have
// no menu bar, and treating one as a desktop is what puts a crashing
// tray into a headless Mac's daemon start.
func TestIsAquaSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  string
		want bool
	}{
		{"a desktop login", "Aqua\n", true},
		{"no trailing newline", "Aqua", true},
		{"over SSH", "Background\n", false},
		{"a LaunchDaemon", "System\n", false},
		{"the standard I/O domain", "StandardIO\n", false},
		{"nothing at all", "", false},
		{"only whitespace", "   \n", false},
		// Matched exactly: a name that merely contains Aqua is a
		// different domain, not a desktop.
		{"a lookalike", "NotAqua\n", false},
		// launchctl's names are capitalized; a lowercase answer is not
		// something it prints, so it is not something to accept.
		{"lowercased", "aqua\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAquaSession(tc.out); got != tc.want {
				t.Errorf("isAquaSession(%q) = %v, want %v", tc.out, got, tc.want)
			}
		})
	}
}
