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
	Status      JobStatus
	BytesDone   int64
	BytesTotal  int64
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
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Store struct {
	db *sql.DB
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
	bytes_done, bytes_total, resume_offset, info_hash, resolved_paths, error_msg, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.ID, j.Type, j.Source, j.Output, j.Format, j.Concurrency, j.Status,
		j.BytesDone, j.BytesTotal, j.ResumeOffset, j.InfoHash, string(resolved), j.ErrorMsg,
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
	bytes_done=?, bytes_total=?, resume_offset=?, info_hash=?, resolved_paths=?, error_msg=?, updated_at=?
WHERE id=?`,
		j.Type, j.Source, j.Output, j.Format, j.Concurrency, j.Status,
		j.BytesDone, j.BytesTotal, j.ResumeOffset, j.InfoHash, string(resolved), j.ErrorMsg,
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
	bytes_done, bytes_total, resume_offset, info_hash, resolved_paths, error_msg, created_at, updated_at
FROM jobs WHERE id=?`, id)
	return scanJob(row)
}

func (s *Store) ListJobs(ctx context.Context) ([]*Job, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, type, source, output, format, concurrency, status,
	bytes_done, bytes_total, resume_offset, info_hash, resolved_paths, error_msg, created_at, updated_at
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
		&j.Status, &j.BytesDone, &j.BytesTotal, &j.ResumeOffset, &j.InfoHash, &resolved, &j.ErrorMsg,
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
