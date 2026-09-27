package daemon

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/time/rate"

	"godl/internal/prefs"
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
	if err := prefs.Validate(s); err != nil {
		return store.Settings{}, err
	}
	// Created now rather than at the first download, so a path that
	// can't be used is refused while the user is still looking at it.
	if s.DownloadDir != "" {
		if err := os.MkdirAll(s.DownloadDir, 0o755); err != nil {
			return store.Settings{}, fmt.Errorf("download folder: %w", err)
		}
	}
	if err := d.st.SaveSettings(ctx, s); err != nil {
		return store.Settings{}, err
	}
	d.setCachedSettings(s)
	d.rebuildGlobalLimiter(s)
	d.tryStartQueued()
	return s, nil
}
