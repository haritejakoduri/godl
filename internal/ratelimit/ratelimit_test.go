package ratelimit

import (
	"context"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

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

// TestWaitAllBlocksOnAnyExhaustedLimiter is the regression test for the
// global-bandwidth-cap feature: WaitAll must actually wait on every
// non-nil limiter passed to it, not just the first (or a random one) —
// a job's own per-job cap and the Settings tab's shared global cap are
// both real constraints, and one being wide open must not let a
// request through while the other is exhausted.
func TestWaitAllBlocksOnAnyExhaustedLimiter(t *testing.T) {
	wideOpen := rate.NewLimiter(rate.Inf, 0)
	exhausted := rate.NewLimiter(rate.Limit(0.0001), 1) // ~1 token per ~3 hours
	exhausted.AllowN(time.Now(), 1)                     // drain the one burst token

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// wideOpen first, exhausted second — proves WaitAll doesn't stop
	// checking after the first limiter allows the request through. (Not
	// asserting the exact error: rate.Limiter.WaitN returns its own
	// "would exceed context deadline" error rather than wrapping
	// context.DeadlineExceeded — the failure itself is what matters
	// here, proving it actually consulted the exhausted limiter instead
	// of returning nil as soon as wideOpen allowed it.)
	if err := WaitAll(ctx, 1, wideOpen, exhausted); err == nil {
		t.Fatal("WaitAll with an exhausted limiter anywhere in the list = nil, want an error (it should have blocked on it)")
	}
}

func TestWaitAllSkipsNilLimitersAndNonPositiveN(t *testing.T) {
	if err := WaitAll(context.Background(), 100, nil, nil); err != nil {
		t.Fatalf("WaitAll with only nil limiters = %v, want nil (unlimited)", err)
	}

	// n<=0 must be a no-op even against a limiter that would otherwise
	// block indefinitely.
	exhausted := rate.NewLimiter(rate.Limit(0.0001), 1)
	exhausted.AllowN(time.Now(), 1)
	if err := WaitAll(context.Background(), 0, exhausted); err != nil {
		t.Fatalf("WaitAll with n=0 = %v, want nil (no-op)", err)
	}
}
