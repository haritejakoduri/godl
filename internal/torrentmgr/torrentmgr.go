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
)

type Manager struct {
	client *torrent.Client

	mu     sync.Mutex
	active map[string]*torrent.Torrent // jobID -> live torrent
}

func New(dataDir string) (*Manager, error) {
	cfg := torrent.NewDefaultClientConfig()
	cfg.DataDir = dataDir
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
	return &Manager{client: cl, active: map[string]*torrent.Torrent{}}, nil
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
	if strings.HasPrefix(source, "magnet:") {
		return torrent.TorrentSpecFromMagnetUri(source)
	}
	mi, err := metainfo.LoadFromFile(source)
	if err != nil {
		return nil, fmt.Errorf("loading torrent file: %w", err)
	}
	return torrent.TorrentSpecFromMetaInfo(mi), nil
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
