// Package store persists job state in a local sqlite database
// (modernc.org/sqlite, pure Go, no cgo) so the daemon can survive restarts
// and multiple godl clients see a consistent job list.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

type JobType string

const (
	JobURL     JobType = "url"
	JobSocial  JobType = "social"
	JobTorrent JobType = "torrent"
	JobWebDAV  JobType = "webdav"
)

type JobStatus string

const (
	StatusQueued    JobStatus = "queued"
	StatusActive    JobStatus = "active"
	StatusPaused    JobStatus = "paused"
	StatusCompleted JobStatus = "completed"
	StatusFailed    JobStatus = "failed"
	StatusCanceled  JobStatus = "canceled"
)

// Job is one download task, of whatever type. Fields not relevant to a
// given type are left zero (e.g. Format only applies to social jobs).
type Job struct {
	ID          string
	Type        JobType
	Source      string // URL, magnet link, or .torrent file path
	Output      string // destination file (url) or directory (social/torrent)
	Format      string // yt-dlp -f value, social jobs only
	Concurrency int    // url jobs only
	// LimitRate caps this job's own transfer at this many bytes/second
	// (0 = unlimited). Persisted so pause/resume/retry reapply the same
	// cap the job was started with instead of silently going unlimited.
	// Torrent jobs share one client-wide limiter (see internal/torrentmgr)
	// rather than a true per-job one — the last torrent job to set this
	// wins for all of them, a limitation of the underlying torrent
	// library, not of this field.
	LimitRate int64
	// Sha256 is the expected hex digest for url jobs (empty = no
	// verification). Persisted so pause/resume/retry re-verify against
	// the same value the job was started with. See internal/downloader's
	// verifyChecksum for why a mismatch means the whole file is
	// redownloaded rather than repaired: the digest covers the whole
	// file, so there's no way to know which part is bad.
	Sha256     string
	Status     JobStatus
	BytesDone  int64
	BytesTotal int64
	// ResumeOffset is the confirmed-contiguous byte offset for url jobs
	// (single-stream path). Concurrent chunked url jobs track resume state
	// in a sidecar file next to the output instead (see internal/downloader).
	ResumeOffset int64
	// InfoHash identifies a torrent job for logging/dedup. Torrent resume
	// itself doesn't need it: anacrolix/torrent re-verifies whatever
	// piece data already exists on disk when the torrent is re-added.
	InfoHash string
	// ResolvedPaths holds the actual file(s) written to disk, for job
	// types where Output is a directory rather than the exact file:
	// the torrent's content path (Output/<torrent name>) once its info
	// is known, or each file yt-dlp actually produced (its own naming,
	// and only the final post-merge/post-processed path — not
	// intermediate video/audio streams that get deleted after
	// merging). url jobs don't need this: Output already is the exact
	// file. Used by "godl remove --purge" to know what to delete.
	ResolvedPaths []string
	ErrorMsg      string
	// RetryCount is how many times the daemon has auto-retried this job
	// since its last success (see Settings.AutoRetry). A manual "godl
	// retry" resets it to 0 — it tracks the automated backoff streak,
	// not a lifetime total. Always 0 unless auto-retry is/was enabled.
	RetryCount int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Settings holds the daemon's user-configurable defaults, edited from
// the TUI's Settings tab (or godl settings) and applied to every job
// from then on until changed again. Stored as individual key/value rows
// (see the settings table) rather than one JSON blob, so a future field
// doesn't need a data migration for existing rows — GetSettings just
// falls back to the zero-ish default below for any key that isn't set.
type Settings struct {
	// MaxConcurrent caps how many jobs run at once, across every job
	// type combined; jobs beyond the cap stay queued and start as
	// running ones finish. 0 means unlimited (the historical behavior,
	// and the default).
	MaxConcurrent int
	// DefaultRateLimit is applied to a new job that doesn't pass its
	// own --limit-rate, in the same syntax that flag accepts (e.g.
	// "2M"); "" means unlimited.
	DefaultRateLimit string
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

// DefaultSettings is what GetSettings returns before anything has ever
// been saved — unlimited concurrency and rate, no auto-retry, no
// notifications, matching godl's behavior before this feature existed.
func DefaultSettings() Settings {
	return Settings{AutoRetryMaxAttempts: 3}
}

type Store struct {
	db *sql.DB

	// resolvedMu serializes AppendResolvedPath's read-modify-write
	// (read the current list, append, write the whole list back) across
	// goroutines. db.SetMaxOpenConns(1) below only serializes individual
	// statements, not this multi-statement sequence — without this,
	// concurrent callers (e.g. a WebDAV folder job downloading several
	// files at once) can each read the same pre-append list and then
	// both write, and whichever write lands second silently discards
	// the other's entry.
	resolvedMu sync.Mutex
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// sql.Open is lazy (no file created yet); force it now so the
	// permission tightening below actually has a file to act on. The
	// containing directory (paths.DataDir, 0700) is the primary
	// protection — job records carry full source URLs, which can
	// include auth tokens — this chmod is defense in depth in case
	// that file ever ends up somewhere less restrictive.
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	os.Chmod(path, 0o600)
	// sqlite handles one writer at a time; the daemon is single-process
	// but multiple goroutines touch the DB concurrently.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON;`); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS jobs (
	id            TEXT PRIMARY KEY,
	type          TEXT NOT NULL,
	source        TEXT NOT NULL,
	output        TEXT NOT NULL,
	format        TEXT NOT NULL DEFAULT '',
	concurrency   INTEGER NOT NULL DEFAULT 1,
	status        TEXT NOT NULL,
	bytes_done    INTEGER NOT NULL DEFAULT 0,
	bytes_total   INTEGER NOT NULL DEFAULT 0,
	resume_offset INTEGER NOT NULL DEFAULT 0,
	info_hash     TEXT NOT NULL DEFAULT '',
	error_msg     TEXT NOT NULL DEFAULT '',
	created_at    INTEGER NOT NULL,
	updated_at    INTEGER NOT NULL
);
`); err != nil {
		return err
	}

	// resolved_paths was added after jobs shipped without it; add it to
	// existing databases rather than assuming a fresh CREATE TABLE.
	hasCol, err := s.hasColumn("jobs", "resolved_paths")
	if err != nil {
		return err
	}
	if !hasCol {
		if _, err := s.db.Exec(`ALTER TABLE jobs ADD COLUMN resolved_paths TEXT NOT NULL DEFAULT '[]'`); err != nil {
			return err
		}
	}

	// limit_rate was added after jobs shipped without it, same as
	// resolved_paths above.
	hasCol, err = s.hasColumn("jobs", "limit_rate")
	if err != nil {
		return err
	}
	if !hasCol {
		if _, err := s.db.Exec(`ALTER TABLE jobs ADD COLUMN limit_rate INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}

	// sha256 was added after jobs shipped without it, same as
	// resolved_paths above.
	hasCol, err = s.hasColumn("jobs", "sha256")
	if err != nil {
		return err
	}
	if !hasCol {
		if _, err := s.db.Exec(`ALTER TABLE jobs ADD COLUMN sha256 TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}

	// retry_count was added after jobs shipped without it, same as
	// resolved_paths above.
	hasCol, err = s.hasColumn("jobs", "retry_count")
	if err != nil {
		return err
	}
	if !hasCol {
		if _, err := s.db.Exec(`ALTER TABLE jobs ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}

	if _, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS settings (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`); err != nil {
		return err
	}
	return nil
}

func (s *Store) hasColumn(table, col string) (bool, error) {
	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, ctype string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// NewID returns a short, unique job id (8 hex chars), retrying on the
// astronomically unlikely collision.
func (s *Store) NewID(ctx context.Context) (string, error) {
	for i := 0; i < 10; i++ {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		id := hex.EncodeToString(b[:])
		if _, err := s.GetJob(ctx, id); err == sql.ErrNoRows {
			return id, nil
		}
	}
	return "", fmt.Errorf("could not allocate a unique job id")
}

func (s *Store) CreateJob(ctx context.Context, j *Job) error {
	now := time.Now()
	j.CreatedAt, j.UpdatedAt = now, now
	resolved, err := json.Marshal(j.ResolvedPaths)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO jobs (id, type, source, output, format, concurrency, status,
	bytes_done, bytes_total, resume_offset, info_hash, resolved_paths, error_msg, limit_rate, sha256, retry_count, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.Type, j.Source, j.Output, j.Format, j.Concurrency, j.Status,
		j.BytesDone, j.BytesTotal, j.ResumeOffset, j.InfoHash, string(resolved), j.ErrorMsg, j.LimitRate, j.Sha256, j.RetryCount,
		j.CreatedAt.Unix(), j.UpdatedAt.Unix())
	return err
}

func (s *Store) UpdateJob(ctx context.Context, j *Job) error {
	j.UpdatedAt = time.Now()
	resolved, err := json.Marshal(j.ResolvedPaths)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
UPDATE jobs SET type=?, source=?, output=?, format=?, concurrency=?, status=?,
	bytes_done=?, bytes_total=?, resume_offset=?, info_hash=?, resolved_paths=?, error_msg=?, limit_rate=?, sha256=?, retry_count=?, updated_at=?
WHERE id=?`,
		j.Type, j.Source, j.Output, j.Format, j.Concurrency, j.Status,
		j.BytesDone, j.BytesTotal, j.ResumeOffset, j.InfoHash, string(resolved), j.ErrorMsg, j.LimitRate, j.Sha256, j.RetryCount,
		j.UpdatedAt.Unix(), j.ID)
	return err
}

// UpdateProgress is a lightweight update for the frequent byte-count ticks
// that happen during an active download, avoiding a full row rewrite.
func (s *Store) UpdateProgress(ctx context.Context, id string, bytesDone, bytesTotal int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET bytes_done=?, bytes_total=?, updated_at=? WHERE id=?`,
		bytesDone, bytesTotal, time.Now().Unix(), id)
	return err
}

// UpdateResumeOffset checkpoints the confirmed-contiguous byte offset for
// a single-stream url job while it's actively downloading, so an
// ungraceful daemon death (kill, crash, reboot) loses at most the last
// tick's worth of progress instead of the whole job. Concurrent chunked
// downloads don't need this: they checkpoint to their own sidecar file.
func (s *Store) UpdateResumeOffset(ctx context.Context, id string, offset int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET resume_offset=?, updated_at=? WHERE id=?`,
		offset, time.Now().Unix(), id)
	return err
}

func (s *Store) UpdateStatus(ctx context.Context, id string, status JobStatus, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE jobs SET status=?, error_msg=?, updated_at=? WHERE id=?`,
		status, errMsg, time.Now().Unix(), id)
	return err
}

// AppendResolvedPath records another actual on-disk file for a job whose
// Output is a directory (social, occasionally playlists producing more
// than one final file). No-ops if path is already recorded.
func (s *Store) AppendResolvedPath(ctx context.Context, id, path string) error {
	s.resolvedMu.Lock()
	defer s.resolvedMu.Unlock()

	j, err := s.GetJob(ctx, id)
	if err != nil {
		return err
	}
	for _, p := range j.ResolvedPaths {
		if p == path {
			return nil
		}
	}
	j.ResolvedPaths = append(j.ResolvedPaths, path)
	resolved, err := json.Marshal(j.ResolvedPaths)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE jobs SET resolved_paths=?, updated_at=? WHERE id=?`,
		string(resolved), time.Now().Unix(), id)
	return err
}

func (s *Store) DeleteJob(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM jobs WHERE id=?`, id)
	return err
}

func (s *Store) GetJob(ctx context.Context, id string) (*Job, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, type, source, output, format, concurrency, status,
	bytes_done, bytes_total, resume_offset, info_hash, resolved_paths, error_msg, limit_rate, sha256, retry_count, created_at, updated_at
FROM jobs WHERE id=?`, id)
	return scanJob(row)
}

func (s *Store) ListJobs(ctx context.Context) ([]*Job, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, type, source, output, format, concurrency, status,
	bytes_done, bytes_total, resume_offset, info_hash, resolved_paths, error_msg, limit_rate, sha256, retry_count, created_at, updated_at
FROM jobs ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []*Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanJob(row scanner) (*Job, error) {
	var j Job
	var created, updated int64
	var resolved string
	if err := row.Scan(&j.ID, &j.Type, &j.Source, &j.Output, &j.Format, &j.Concurrency,
		&j.Status, &j.BytesDone, &j.BytesTotal, &j.ResumeOffset, &j.InfoHash, &resolved, &j.ErrorMsg, &j.LimitRate, &j.Sha256, &j.RetryCount,
		&created, &updated); err != nil {
		return nil, err
	}
	j.CreatedAt = time.Unix(created, 0)
	j.UpdatedAt = time.Unix(updated, 0)
	if resolved != "" {
		if err := json.Unmarshal([]byte(resolved), &j.ResolvedPaths); err != nil {
			return nil, fmt.Errorf("decoding resolved_paths: %w", err)
		}
	}
	return &j, nil
}

// settingsKeys names every row GetSettings/SaveSettings read and write
// in the settings table, so both stay in sync with Settings' fields by
// construction instead of by convention.
const (
	settingsKeyMaxConcurrent        = "max_concurrent"
	settingsKeyDefaultRateLimit     = "default_rate_limit"
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
