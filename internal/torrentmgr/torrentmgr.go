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
	"time"

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
	// wanted holds a job's selected files once its metadata is known;
	// absent means the whole torrent. See Select.
	wanted map[string][]*torrent.File
}

// FileInfo describes one file of a torrent.
type FileInfo struct {
	Index  int // 0-based
	Path   string
	Length int64
	Done   int64
}

// unlimitedBurst is large enough that the rate limiter never itself
// throttles a burst below Inf/no-limit — only SetDownloadLimit's
// non-default rate does that (via its own, tighter burst).
const unlimitedBurst = 64 * 1024 * 1024

func New(dataDir string) (*Manager, error) {
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dataDir
	return newManager(cfg)
}

func newManager(cfg *torrent.ClientConfig) (*Manager, error) {
	dlLimiter := rate.NewLimiter(rate.Inf, unlimitedBurst)
	cfg.DownloadRateLimiter = dlLimiter
	// Keep uploading after completion. Only jobs with a seed ratio/time
	// stay in the client once done (see Daemon.startSeeding); the rest
	// are dropped at completion, so this doesn't make every finished
	// torrent seed forever.
	cfg.Seed = true
	cl, err := torrent.NewClient(cfg)
	if err != nil && strings.Contains(err.Error(), "address already in use") {
		// The default BitTorrent port is taken — another torrent
		// client, or another user's godl on the same machine. Any free
		// port works for an outgoing-first client, and the alternative
		// is the whole daemon failing to start over it.
		cfg.ListenPort = 0
		cl, err = torrent.NewClient(cfg)
	}
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
	return &Manager{client: cl, dlLimiter: dlLimiter, active: map[string]*torrent.Torrent{}, wanted: map[string][]*torrent.File{}}, nil
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

// Add registers source (a magnet link or path to a .torrent file) for
// downloading into outputDir. anacrolix places the torrent's content
// under outputDir/<torrent name>/. Nothing downloads until Select is
// called, once the metadata has arrived (t.GotInfo()).
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
	return t, nil
}

// Select starts downloading the files sel picks (all of them for a nil
// sel) and deprioritizes the rest. Call it after GotInfo. It returns
// how many files were selected; zero means sel matched nothing and
// nothing will download.
func (m *Manager) Select(jobID string, sel *Selection) (int, error) {
	m.mu.Lock()
	t, ok := m.active[jobID]
	m.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("torrent job %s is not running", jobID)
	}
	if sel == nil {
		m.mu.Lock()
		delete(m.wanted, jobID)
		m.mu.Unlock()
		t.DownloadAll()
		return len(t.Files()), nil
	}
	var wanted []*torrent.File
	for i, f := range t.Files() {
		if sel.Match(i+1, f.DisplayPath()) {
			wanted = append(wanted, f)
			f.Download()
		} else {
			f.SetPriority(torrent.PiecePriorityNone)
		}
	}
	m.mu.Lock()
	m.wanted[jobID] = wanted
	m.mu.Unlock()
	return len(wanted), nil
}

// torrentInfoRetry is how often ListFiles re-checks for metadata.
const torrentInfoRetry = 100 * time.Millisecond

// ListFiles fetches source's metadata — from peers, for a magnet link —
// and lists its files without downloading any content. A torrent that
// a running job already holds is read, never dropped.
func (m *Manager) ListFiles(source string, timeout time.Duration) (name string, files []FileInfo, err error) {
	spec, err := specFromSource(source)
	if err != nil {
		return "", nil, err
	}
	t, existed := m.client.Torrent(spec.InfoHash)
	if !existed {
		t, _, err = m.client.AddTorrentSpec(spec)
		if err != nil {
			return "", nil, err
		}
		defer t.Drop()
	}
	select {
	case <-t.GotInfo():
	case <-time.After(timeout):
		return "", nil, fmt.Errorf("timed out after %s fetching the torrent's metadata from peers", timeout)
	}
	return t.Info().BestName(), fileInfos(t.Files()), nil
}

func fileInfos(fs []*torrent.File) []FileInfo {
	out := make([]FileInfo, len(fs))
	for i, f := range fs {
		out[i] = FileInfo{Index: i, Path: f.DisplayPath(), Length: f.Length(), Done: f.BytesCompleted()}
	}
	return out
}

// Files lists a running job's files, or nil if its metadata hasn't
// arrived yet. selected is parallel to it: which ones the job downloads.
func (m *Manager) Files(jobID string) (files []FileInfo, selected []bool) {
	m.mu.Lock()
	t, ok := m.active[jobID]
	wanted, partial := m.wanted[jobID]
	m.mu.Unlock()
	if !ok || t.Info() == nil {
		return nil, nil
	}
	all := t.Files()
	files = fileInfos(all)
	isWanted := make(map[*torrent.File]bool, len(wanted))
	for _, w := range wanted {
		isWanted[w] = true
	}
	selected = make([]bool, len(all))
	for i, f := range all {
		selected[i] = !partial || isWanted[f]
	}
	return files, selected
}

// File returns a running job's file by 0-based index, for streaming.
func (m *Manager) File(jobID string, index int) (*torrent.File, bool) {
	m.mu.Lock()
	t, ok := m.active[jobID]
	m.mu.Unlock()
	if !ok || t.Info() == nil {
		return nil, false
	}
	fs := t.Files()
	if index < 0 || index >= len(fs) {
		return nil, false
	}
	return fs[index], true
}

// Done reports whether every selected file of a running job is complete.
func (m *Manager) Done(jobID string) bool {
	done, total, ok := m.Progress(jobID)
	return ok && total > 0 && done >= total
}

// Uploaded returns how many content bytes a running job has sent to
// peers since it was added.
func (m *Manager) Uploaded(jobID string) int64 {
	m.mu.Lock()
	t, ok := m.active[jobID]
	m.mu.Unlock()
	if !ok {
		return 0
	}
	st := t.Stats()
	return st.BytesWrittenData.Int64()
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
	delete(m.wanted, jobID)
	m.mu.Unlock()
	if ok {
		t.Drop()
	}
}

// Cancel behaves like Pause; the daemon is responsible for cleaning up any
// on-disk data if the job is truly being discarded rather than paused.
func (m *Manager) Cancel(jobID string) { m.Pause(jobID) }

// Progress reports bytes completed / total for an active torrent job,
// counting only its selected files when it has a selection. total is 0
// until the torrent's metainfo has been fetched from peers.
func (m *Manager) Progress(jobID string) (done, total int64, ok bool) {
	m.mu.Lock()
	t, exists := m.active[jobID]
	wanted, partial := m.wanted[jobID]
	m.mu.Unlock()
	if !exists {
		return 0, 0, false
	}
	if t.Info() == nil {
		return 0, 0, true
	}
	if !partial {
		return t.BytesCompleted(), t.Length(), true
	}
	for _, f := range wanted {
		done += f.BytesCompleted()
		total += f.Length()
	}
	return done, total, true
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

// InfoHashOf reads a magnet link's or .torrent file's info hash (hex)
// without touching the network.
func InfoHashOf(source string) (string, error) {
	spec, err := specFromSource(source)
	if err != nil {
		return "", err
	}
	return spec.InfoHash.HexString(), nil
}
