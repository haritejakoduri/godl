package daemon

import (
	"context"
	"errors"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"godl/internal/paths"
	"godl/internal/store"
	"godl/internal/torrentmgr"
)

type Daemon struct {
	st      *store.Store
	tm      *torrentmgr.Manager
	dataDir string

	mu       sync.Mutex
	runtimes map[string]*runtime

	logMu   sync.Mutex
	logSubs map[chan logMsg]struct{}

	// Cache of the store's settings table, read on every job
	// start/finish so those paths don't hit sqlite. Refreshed on save.
	settingsMu sync.RWMutex
	settings   store.Settings

	// Both derived from settings.GlobalRateLimit; see its doc comment in
	// internal/store for what each job type does with it. globalLimiter
	// is one shared instance — handing the same *rate.Limiter to every
	// url/webdav job is what makes their combined throughput share one
	// bucket. globalRateLimitBps is the same cap as a number, for the
	// job types that can't draw from that bucket. 0/nil means unlimited.
	globalMu           sync.RWMutex
	globalLimiter      *rate.Limiter
	globalRateLimitBps int64

	// See tryStartQueued for why it can be re-entered.
	tryMu      sync.Mutex
	tryRunning bool
	tryAgain   bool

	// Pending auto-retry timers, tracked so Close can stop them rather
	// than letting one fire after the store it writes to is closed.
	retryMu     sync.Mutex
	retryTimers map[string]*time.Timer
}

func NewDaemon() (*Daemon, error) {
	dataDir, err := paths.DataDir()
	if err != nil {
		return nil, err
	}
	dbPath, err := paths.DBPath()
	if err != nil {
		return nil, err
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return nil, err
	}
	torrentDir, err := paths.TorrentDataDir()
	if err != nil {
		return nil, err
	}
	tm, err := torrentmgr.New(torrentDir)
	if err != nil {
		st.Close()
		return nil, err
	}
	settings, err := st.GetSettings(context.Background())
	if err != nil {
		tm.Close()
		st.Close()
		return nil, err
	}
	d := &Daemon{
		st:          st,
		tm:          tm,
		dataDir:     dataDir,
		runtimes:    map[string]*runtime{},
		logSubs:     map[chan logMsg]struct{}{},
		settings:    settings,
		retryTimers: map[string]*time.Timer{},
	}
	d.rebuildGlobalLimiter(settings)
	return d, nil
}

// Serve accepts connections on the Unix socket until the listener closes.
// On startup it resumes any job that was left active/queued from a
// previous run (the process died or the machine restarted).
func (d *Daemon) Serve() error {
	sockPath, err := paths.SocketPath()
	if err != nil {
		return err
	}
	os.Remove(sockPath) // stale socket from a killed daemon

	l, err := net.Listen("unix", sockPath)
	if err != nil {
		return err
	}
	defer l.Close()
	defer os.Remove(sockPath)
	// Unix sockets get created with a mode based on umask (often
	// world-connectable), and SocketPath() can fall back to a shared
	// temp dir when $XDG_RUNTIME_DIR isn't set — restrict explicitly so
	// another local user can never issue commands to this daemon
	// (read job URLs/tokens, start/cancel downloads as this user)
	// regardless of which directory it ends up in.
	if err := os.Chmod(sockPath, 0o600); err != nil {
		return err
	}

	d.resumeInterruptedJobs()

	for {
		conn, err := l.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			log.Printf("accept: %v", err)
			continue
		}
		go d.handleConn(conn)
	}
}

func (d *Daemon) resumeInterruptedJobs() {
	ctx := context.Background()
	jobs, err := d.st.ListJobs(ctx)
	if err != nil {
		log.Printf("resume scan: %v", err)
		return
	}
	for _, j := range jobs {
		if j.Status != store.StatusActive && j.Status != store.StatusQueued {
			continue
		}
		log.Printf("resuming interrupted job %s (%s)", j.ID, j.Type)
		// Normalize to Queued before dispatch: start() may leave it
		// there rather than actually running it (MaxConcurrent already
		// full from an earlier job in this same loop), and a job stuck
		// showing "active" while nothing is running it would be wrong —
		// tryStartQueued only ever looks for StatusQueued.
		if j.Status != store.StatusQueued {
			j.Status = store.StatusQueued
			if err := d.st.UpdateJob(ctx, j); err != nil {
				log.Printf("resume scan: updating %s to queued: %v", j.ID, err)
				continue
			}
		}
		d.start(j)
	}
}

func (d *Daemon) Close() {
	d.retryMu.Lock()
	for _, t := range d.retryTimers {
		t.Stop()
	}
	d.retryTimers = nil
	d.retryMu.Unlock()
	d.tm.Close()
	d.st.Close()
}
