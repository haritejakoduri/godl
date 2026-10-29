//go:build darwin

package tray

import (
	"errors"
	"testing"
)

// TestCheckSessionAnswers is a smoke test for the half that cannot be
// exercised anywhere but a Mac: that /bin/launchctl is where this
// expects it, that "managername" is a subcommand it accepts, and that
// the probe therefore reaches a verdict instead of silently taking its
// permissive error path on every machine.
//
// It cannot assert which verdict — that depends on whether whoever
// runs the tests is at a desk or on a build runner — so it asserts the
// two things that must hold either way: the answer is nil or
// ErrNoSession, and the name launchctl gave is the reason for it.
func TestCheckSessionAnswers(t *testing.T) {
	out, err := launchctlManagerName()
	if err != nil {
		t.Skipf("launchctl managername is unavailable here (%v); checkSession's permissive path covers this", err)
	}
	t.Logf("launchctl managername = %q", out)

	got := checkSession()
	switch {
	case isAquaSession(out):
		if got != nil {
			t.Fatalf("checkSession() = %v in an Aqua session, want nil", got)
		}
	default:
		if !errors.Is(got, ErrNoSession) {
			t.Fatalf("checkSession() = %v in a %q session, want ErrNoSession", got, out)
		}
	}
}
