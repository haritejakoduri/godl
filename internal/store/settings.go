// Package store persists job state in a local sqlite database
// (modernc.org/sqlite, pure Go, no cgo) so the daemon can survive restarts
// and multiple godl clients see a consistent job list.
package store

import (
	"context"
	"strconv"

	_ "modernc.org/sqlite"
)

// Settings are the daemon's user-configurable defaults, applied to every
// job started from then on. Stored as key/value rows rather than one
// JSON blob, so adding a field needs no migration: GetSettings falls back
// to DefaultSettings for any key that isn't set.
type Settings struct {
	// MaxConcurrent caps how many jobs run at once, across every job
	// type combined; jobs beyond the cap stay queued and start as
	// running ones finish. 0 means unlimited (the historical behavior,
	// and the default).
	MaxConcurrent int
	// DefaultRateLimit is applied to a new job that doesn't pass its
	// own --limit-rate, in the same syntax that flag accepts (e.g.
	// "2M"); "" means unlimited. Per-job: three jobs each falling back
	// to this default can still add up to 3x it combined — see
	// GlobalRateLimit for a shared combined ceiling instead.
	DefaultRateLimit string
	// Caps every job's transfer combined; "" means unlimited. url and
	// webdav jobs share one real token bucket. Torrent and social can't
	// (anacrolix takes its own client-wide limiter; yt-dlp is a
	// subprocess), so each is capped individually instead — meaning
	// combined throughput can exceed this when those run alongside.
	GlobalRateLimit string
	// AutoRetry, when true, automatically re-queues a job that fails
	// (not one that's paused/canceled) after a backoff delay, up to
	// AutoRetryMaxAttempts times, instead of leaving it failed until a
	// manual "godl retry".
	AutoRetry            bool
	AutoRetryMaxAttempts int
	// NotifyOnComplete fires a best-effort desktop notification
	// (internal/notify) when a job completes successfully.
	NotifyOnComplete bool
}

// DefaultSettings is what GetSettings returns before anything is saved.
func DefaultSettings() Settings {
	return Settings{AutoRetryMaxAttempts: 3}
}

// settingsKeys names every row GetSettings/SaveSettings read and write
// in the settings table, so both stay in sync with Settings' fields by
// construction instead of by convention.
const (
	settingsKeyMaxConcurrent        = "max_concurrent"
	settingsKeyDefaultRateLimit     = "default_rate_limit"
	settingsKeyGlobalRateLimit      = "global_rate_limit"
	settingsKeyAutoRetry            = "auto_retry"
	settingsKeyAutoRetryMaxAttempts = "auto_retry_max_attempts"
	settingsKeyNotifyOnComplete     = "notify_on_complete"
)

// GetSettings reads the daemon's saved settings, falling back to
// DefaultSettings() for any key that's never been written (a fresh
// database, or one from before this feature existed) — there's no
// separate "has this ever been configured" migration step, a missing
// key just means "use the default".
func (s *Store) GetSettings(ctx context.Context) (Settings, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return Settings{}, err
	}
	defer rows.Close()
	kv := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return Settings{}, err
		}
		kv[k] = v
	}
	if err := rows.Err(); err != nil {
		return Settings{}, err
	}

	set := DefaultSettings()
	if v, ok := kv[settingsKeyMaxConcurrent]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			set.MaxConcurrent = n
		}
	}
	if v, ok := kv[settingsKeyDefaultRateLimit]; ok {
		set.DefaultRateLimit = v
	}
	if v, ok := kv[settingsKeyGlobalRateLimit]; ok {
		set.GlobalRateLimit = v
	}
	if v, ok := kv[settingsKeyAutoRetry]; ok {
		set.AutoRetry = v == "true"
	}
	if v, ok := kv[settingsKeyAutoRetryMaxAttempts]; ok {
		if n, err := strconv.Atoi(v); err == nil {
			set.AutoRetryMaxAttempts = n
		}
	}
	if v, ok := kv[settingsKeyNotifyOnComplete]; ok {
		set.NotifyOnComplete = v == "true"
	}
	return set, nil
}

// SaveSettings persists every field of set, upserting each key/value
// row. Callers (daemon.applySettings) are expected to validate set
// first — this just writes whatever it's given.
func (s *Store) SaveSettings(ctx context.Context, set Settings) error {
	kv := map[string]string{
		settingsKeyMaxConcurrent:        strconv.Itoa(set.MaxConcurrent),
		settingsKeyDefaultRateLimit:     set.DefaultRateLimit,
		settingsKeyGlobalRateLimit:      set.GlobalRateLimit,
		settingsKeyAutoRetry:            strconv.FormatBool(set.AutoRetry),
		settingsKeyAutoRetryMaxAttempts: strconv.Itoa(set.AutoRetryMaxAttempts),
		settingsKeyNotifyOnComplete:     strconv.FormatBool(set.NotifyOnComplete),
	}
	for k, v := range kv {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,
			k, v); err != nil {
			return err
		}
	}
	return nil
}
