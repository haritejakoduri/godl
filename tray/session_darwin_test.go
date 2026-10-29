//go:build darwin

package tray

import (
	"errors"
	"strings"
	"testing"
)

// TestCheckSessionAnswers covers the half that cannot be exercised
// anywhere but a Mac: that /bin/launchctl is where this expects it,
// that "managername" is a subcommand it accepts, and that the probe
// therefore reaches a verdict.
//
// It fails rather than skips when launchctl will not answer. The
// permissive path in checkSession is there so an odd machine still
// gets its tray, but it is a guess, and on a Mac the probe should
// never have to fall back to one — a skip here would read as a pass
// and prove nothing, which is the opposite of why this test exists.
//
// It cannot assert which verdict: that depends on whether whoever runs
// the tests is at a desk or on a build runner. So it asserts the two
// things that hold either way — the answer is nil or ErrNoSession, and
// the name launchctl gave is the reason for it.
func TestCheckSessionAnswers(t *testing.T) {
	out, err := launchctlManagerName()
	if err != nil {
		t.Fatalf("launchctl managername: %v — every macOS daemon start runs this, so it should not be falling back to assuming a desktop", err)
	}
	name := strings.TrimSpace(out)
	t.Logf("launchctl managername = %q", name)

	got := checkSession()
	if isAquaSession(out) {
		if got != nil {
			t.Fatalf("checkSession() = %v in an Aqua session, want nil", got)
		}
		return
	}
	if !errors.Is(got, ErrNoSession) {
		t.Fatalf("checkSession() = %v in a %q session, want ErrNoSession", got, name)
	}
	// The name is in the message because that is how a missing tray
	// gets diagnosed: "godl tray" over SSH says which session it found.
	if !strings.Contains(got.Error(), name) {
		t.Errorf("checkSession() = %q, want it to name the %q session it rejected", got, name)
	}
}
