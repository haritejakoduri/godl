package daemon

import (
	"context"
	"fmt"
	"time"

	"godl/internal/store"
	"godl/internal/torrentmgr"
)

// torrentInfoTimeout bounds how long "godl torrent --list-files" waits
// for a magnet link's metadata to arrive from peers.
const torrentInfoTimeout = 90 * time.Second

func (d *Daemon) startTorrent(j *store.Job) {
	d.launch(j, func(ctx context.Context, rt *runtime) {
		sel, err := torrentmgr.ParseSelection(j.Options.TorrentFiles)
		if err != nil {
			d.finishJob(j.ID, j.BytesDone, false, err)
			return
		}

		// anacrolix/torrent takes one client-wide limiter, not a
		// per-torrent one, so the most recently started torrent job's
		// limit wins for all of them. For the same reason the global cap
		// can't share url/webdav's real bucket (see
		// Daemon.globalRateLimitBps) and is applied as an upper clamp:
		// whichever of this job's rate and the global cap is lower.
		if effective := minPositiveRate(j.LimitRate, d.cachedGlobalRateLimitBps()); effective > 0 {
			d.tm.SetDownloadLimit(effective)
		}

		t, err := d.tm.Add(j.ID, j.Source, j.Output)
		if err != nil {
			d.finishJob(j.ID, j.BytesDone, false, err)
			return
		}

		select {
		case <-t.GotInfo():
			n, err := d.tm.Select(j.ID, sel)
			if err == nil && n == 0 {
				err = fmt.Errorf("--files %q matched none of the torrent's %d file(s) — see \"godl torrent --list-files\"", j.Options.TorrentFiles, len(t.Files()))
			}
			if err != nil {
				d.tm.Cancel(j.ID)
				d.finishJob(j.ID, j.BytesDone, false, err)
				return
			}
			if job, gerr := d.st.GetJob(context.Background(), j.ID); gerr == nil {
				if hex, ok := d.tm.InfoHash(j.ID); ok {
					job.InfoHash = hex
				}
				// The torrent's actual content lands at Output/<name>
				// (single file or a directory, per anacrolix's
				// storage.NewFile convention) — Output itself is just
				// the base directory the user passed with -o. Record
				// the resolved name so "godl remove --purge" knows
				// exactly what to delete without touching anything
				// else in that directory.
				if info := t.Info(); info != nil {
					job.ResolvedPaths = []string{info.Name}
				}
				d.st.UpdateJob(context.Background(), job)
			}
		case <-ctx.Done():
			// pause()/cancel() owns persisting the terminal state.
			return
		}

		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				done, total, ok := d.tm.Progress(j.ID)
				if !ok {
					continue
				}
				d.reportProgress(j.ID, done, total, nil)
				if !d.tm.Done(j.ID) {
					continue
				}
				d.finishJob(j.ID, done, true, nil)
				if j.Options.Seeds() {
					d.startSeeding(j.ID, j.Options)
				} else {
					// Nothing more to do with it; leaving it in the
					// client would keep its peer connections open.
					d.tm.Pause(j.ID)
				}
				return
			}
		}
	})
}
