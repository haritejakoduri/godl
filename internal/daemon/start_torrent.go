package daemon

import (
	"context"
	"time"

	"godl/internal/store"
)

func (d *Daemon) startTorrent(j *store.Job) {
	d.launch(j, func(ctx context.Context, rt *runtime) {
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
			case <-t.Complete().On():
				done, _, _ := d.tm.Progress(j.ID)
				d.finishJob(j.ID, done, true, nil)
				return
			case <-ticker.C:
				done, total, ok := d.tm.Progress(j.ID)
				if ok {
					d.reportProgress(j.ID, done, total, nil)
				}
			}
		}
	})
}
