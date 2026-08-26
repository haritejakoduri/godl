package ratelimit

import "testing"

func TestParseRate(t *testing.T) {
	cases := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"500000", 500000, false},
		{"500", 500, false},
		{"1K", 1024, false},
		{"1k", 1024, false},
		{"500K", 500 * 1024, false},
		{"2M", 2 * 1024 * 1024, false},
		{"1.5M", int64(1.5 * 1024 * 1024), false},
		{"1G", 1024 * 1024 * 1024, false},
		{"  2M  ", 2 * 1024 * 1024, false},
		{"", 0, true},
		{"0", 0, true},
		{"-5", 0, true},
		{"-5K", 0, true},
		{"abc", 0, true},
		{"K", 0, true},
	}
	for _, c := range cases {
		got, err := ParseRate(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseRate(%q) = %d, nil; want an error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRate(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseRate(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestNewLimiterNilForUnlimited(t *testing.T) {
	if l := NewLimiter(0); l != nil {
		t.Errorf("NewLimiter(0) = %v, want nil", l)
	}
	if l := NewLimiter(-1); l != nil {
		t.Errorf("NewLimiter(-1) = %v, want nil", l)
	}
}

func TestNewLimiterBurstFloor(t *testing.T) {
	// A small rate still needs a burst big enough for a single 256KiB
	// (or larger) WaitN call from a download loop not to error out —
	// see minBurst's doc comment.
	l := NewLimiter(100)
	if l == nil {
		t.Fatal("NewLimiter(100) = nil, want a limiter")
	}
	if b := l.Burst(); b < minBurst {
		t.Errorf("Burst() = %d, want at least %d", b, minBurst)
	}
}

func TestNewLimiterBurstMatchesHighRate(t *testing.T) {
	const rate = 5 * mib
	l := NewLimiter(rate)
	if l == nil {
		t.Fatal("NewLimiter = nil, want a limiter")
	}
	if b := l.Burst(); b != rate {
		t.Errorf("Burst() = %d, want %d", b, rate)
	}
}
