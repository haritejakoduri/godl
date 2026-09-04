package mpv

import "testing"

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
