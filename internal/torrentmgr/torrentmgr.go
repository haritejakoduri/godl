// Package torrentmgr wraps anacrolix/torrent (pure Go, no cgo) with the
// small surface godl's daemon needs: add a job by magnet link or .torrent
// file, pause it, resume it, and poll its progress.
//
// Pause/resume doesn't need us to track a piece bitmap ourselves: dropping
// a torrent just detaches it from the client, and re-adding the same
// source against the same output directory makes anacrolix re-verify
// whatever piece data already exists on disk (storage.NewFile's default
// completion check), so completed pieces aren't re-downloaded.
package torrentmgr

import (
	"fmt"
	"strings"
	"sync"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"golang.org/x/time/rate"
)

type Manager struct {
	client *torrent.Client

	// dlLimiter caps download bandwidth across every torrent this
	// client is running — anacrolix/torrent only supports a
	// client-wide rate limiter (ClientConfig.DownloadRateLimiter), not
	// one per torrent, so unlike godl url/webdav's genuinely per-job
	// limiting, a torrent job's --limit-rate is really "set the shared
	// cap all active torrent jobs currently pull against." See
	// SetDownloadLimit.
	dlLimiter *rate.Limiter

	mu     sync.Mutex
	active map[string]*torrent.Torrent // jobID -> live torrent
}

// unlimitedBurst is large enough that the rate limiter never itself
// throttles a burst below Inf/no-limit — only SetDownloadLimit's
// non-default rate does that (via its own, tighter burst).
const unlimitedBurst = 64 * 1024 * 1024

func New(dataDir string) (*Manager, error) {
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dataDir
	dlLimiter := rate.NewLimiter(rate.Inf, unlimitedBurst)
	cfg.DownloadRateLimiter = dlLimiter
	cl, err := torrent.NewClient(cfg)
	if err != nil {
		// The default config listens on both IPv4 and IPv6; on a host/
		// container without IPv6 support at all (common — some VPS
		// images, some Docker network modes, some CI runners) that
		// dual-stack listen fails outright and NewClient errors, which
		// would otherwise take down the whole daemon — url/social/
		// webdav jobs too, not just torrent. Retry IPv4-only before
		// giving up.
		cfg.DisableIPv6 = true
		cl, err = torrent.NewClient(cfg)
		if err != nil {
			return nil, fmt.Errorf("starting torrent client: %w", err)
		}
	}
	return &Manager{client: cl, dlLimiter: dlLimiter, active: map[string]*torrent.Torrent{}}, nil
}

// SetDownloadLimit caps every active (and future) torrent's combined
// download rate at bytesPerSec bytes/second, or removes the cap
// entirely for bytesPerSec <= 0. See the client-wide caveat on
// Manager.dlLimiter: this isn't scoped to one job.
func (m *Manager) SetDownloadLimit(bytesPerSec int64) {
	if bytesPerSec <= 0 {
		m.dlLimiter.SetLimit(rate.Inf)
		m.dlLimiter.SetBurst(unlimitedBurst)
		return
	}
	// Same reasoning as internal/ratelimit's minBurst: the burst has to
	// comfortably cover whatever single read/chunk size anacrolix uses
	// internally, or its WaitN-equivalent calls would error outright
	// instead of just pacing — so it's floored, never set below a
	// generous minimum regardless of how low bytesPerSec itself is.
	const minBurst = 1024 * 1024
	burst := bytesPerSec
	if burst < minBurst {
		burst = minBurst
	}
	m.dlLimiter.SetLimit(rate.Limit(bytesPerSec))
	m.dlLimiter.SetBurst(int(burst))
}

func (m *Manager) Close() {
	m.client.Close()
}

// Add starts (or resumes) downloading source (a magnet link or path to a
// .torrent file) into outputDir. anacrolix places the torrent's content
// under outputDir/<torrent name>/.
func (m *Manager) Add(jobID, source, outputDir string) (*torrent.Torrent, error) {
	spec, err := specFromSource(source)
	if err != nil {
		return nil, err
	}
	spec.Storage = storage.NewFile(outputDir)

	t, _, err := m.client.AddTorrentSpec(spec)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	m.active[jobID] = t
	m.mu.Unlock()

	go func() {
		<-t.GotInfo()
		t.DownloadAll()
	}()

	return t, nil
}

func specFromSource(source string) (*torrent.TorrentSpec, error) {
	var spec *torrent.TorrentSpec
	if strings.HasPrefix(source, "magnet:") {
		s, err := torrent.TorrentSpecFromMagnetUri(source)
		if err != nil {
			return nil, err
		}
		spec = s
	} else {
		mi, err := metainfo.LoadFromFile(source)
		if err != nil {
			return nil, fmt.Errorf("loading torrent file: %w", err)
		}
		spec = torrent.TorrentSpecFromMetaInfo(mi)
	}
	// A degenerate/malformed source (e.g. a magnet link whose btih is
	// all zeros) parses without error but yields a zero info hash, which
	// anacrolix/torrent's AddTorrentSpec panics on rather than erroring —
	// and since a panic here would take down the whole daemon process
	// (see startTorrent's caller), reject it cleanly up front instead.
	if spec.InfoHash.IsZero() {
		return nil, fmt.Errorf("invalid torrent source: empty/zero info hash")
	}
	return spec, nil
}

// Pause drops the torrent from the client, halting network activity.
// Already-written pieces remain on disk for a later Add to pick back up.
func (m *Manager) Pause(jobID string) {
	m.mu.Lock()
	t, ok := m.active[jobID]
	delete(m.active, jobID)
	m.mu.Unlock()
	if ok {
		t.Drop()
	}
}

// Cancel behaves like Pause; the daemon is responsible for cleaning up any
// on-disk data if the job is truly being discarded rather than paused.
func (m *Manager) Cancel(jobID string) { m.Pause(jobID) }

// Progress reports bytes completed / total for an active torrent job.
// total is 0 until the torrent's metainfo has been fetched from peers.
func (m *Manager) Progress(jobID string) (done, total int64, ok bool) {
	m.mu.Lock()
	t, exists := m.active[jobID]
	m.mu.Unlock()
	if !exists {
		return 0, 0, false
	}
	return t.BytesCompleted(), t.Length(), true
}

// InfoHash returns the hex info hash of an active torrent job, if known.
func (m *Manager) InfoHash(jobID string) (string, bool) {
	m.mu.Lock()
	t, exists := m.active[jobID]
	m.mu.Unlock()
	if !exists {
		return "", false
	}
	return t.InfoHash().HexString(), true
}
