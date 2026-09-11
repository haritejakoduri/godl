package daemon

import (
	"context"
	"fmt"

	"golang.org/x/time/rate"

	"godl/internal/ratelimit"
	"godl/internal/store"
)

// rebuildGlobalLimiter swaps in a fresh limiter rather than mutating the
// existing one's rate, so jobs already holding the old instance keep the
// cap they started with — matching how a per-job LimitRate behaves.
func (d *Daemon) rebuildGlobalLimiter(s store.Settings) {
	var bps int64
	if s.GlobalRateLimit != "" {
		if parsed, err := ratelimit.ParseRate(s.GlobalRateLimit); err == nil {
			bps = parsed
		}
	}
	d.globalMu.Lock()
	d.globalLimiter = ratelimit.NewLimiter(bps)
	d.globalRateLimitBps = bps
	d.globalMu.Unlock()
}

func (d *Daemon) cachedGlobalLimiter() *rate.Limiter {
	d.globalMu.RLock()
	defer d.globalMu.RUnlock()
	return d.globalLimiter
}

func (d *Daemon) cachedGlobalRateLimitBps() int64 {
	d.globalMu.RLock()
	defer d.globalMu.RUnlock()
	return d.globalRateLimitBps
}

// minPositiveRate returns the smaller of a and b, treating <=0 as
// "unset" rather than zero, so neither an absent job cap nor an absent
// global cap clobbers the other.
func minPositiveRate(a, b int64) int64 {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	case a < b:
		return a
	default:
		return b
	}
}

func (d *Daemon) cachedSettings() store.Settings {
	d.settingsMu.RLock()
	defer d.settingsMu.RUnlock()
	return d.settings
}

func (d *Daemon) setCachedSettings(s store.Settings) {
	d.settingsMu.Lock()
	d.settings = s
	d.settingsMu.Unlock()
}

// applySettings validates s, persists it, refreshes the in-memory
// cache, and — since MaxConcurrent may have just gone up — tries to
// start whatever's queued. Returns the settings actually saved (equal
// to s on success; validation errors are rejected outright rather than
// silently clamped, so what the caller sees saved is always exactly
// what it asked for).
func (d *Daemon) applySettings(ctx context.Context, s store.Settings) (store.Settings, error) {
	if s.MaxConcurrent < 0 {
		return store.Settings{}, fmt.Errorf("max concurrent downloads can't be negative")
	}
	if s.DefaultRateLimit != "" {
		if _, err := ratelimit.ParseRate(s.DefaultRateLimit); err != nil {
			return store.Settings{}, err
		}
	}
	if s.GlobalRateLimit != "" {
		if _, err := ratelimit.ParseRate(s.GlobalRateLimit); err != nil {
			return store.Settings{}, err
		}
	}
	if s.AutoRetryMaxAttempts < 1 {
		return store.Settings{}, fmt.Errorf("auto-retry max attempts must be at least 1")
	}
	if err := d.st.SaveSettings(ctx, s); err != nil {
		return store.Settings{}, err
	}
	d.setCachedSettings(s)
	d.rebuildGlobalLimiter(s)
	d.tryStartQueued()
	return s, nil
}
