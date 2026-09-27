package daemon

import (
	"context"
	"log"
	"time"

	"godl/internal/store"
)

// seeder keeps one finished torrent uploading until its seed ratio or
// seed time is reached. It runs outside launch/runtime on purpose: a
// seeding torrent shouldn't hold one of the MaxConcurrent download
// slots.
type seeder struct {
	cancel context.CancelFunc
	done   chan struct{}
	stats  seedStats // guarded by Daemon.seedMu
}

type seedStats struct {
	uploadBps float64
	ratio     float64
}

// startSeeding marks job id as seeding and watches its upload until a
// limit in opts is reached, then drops the torrent and marks the job
// completed. Upload made while downloading counts toward the ratio, as
// it does in other clients.
func (d *Daemon) startSeeding(id string, opts store.JobOptions) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &seeder{cancel: cancel, done: make(chan struct{})}
	d.seedMu.Lock()
	if d.seeders == nil {
		cancel()
		d.seedMu.Unlock()
		return // shutting down
	}
	d.seeders[id] = s
	d.seedMu.Unlock()

	if err := d.st.UpdateStatus(context.Background(), id, store.StatusSeeding, ""); err != nil {
		log.Printf("job %s: recording it as seeding failed: %v", id, err)
	}

	go func() {
		defer close(s.done)
		_, size, _ := d.tm.Progress(id)
		start := time.Now()
		lastUp, lastAt := d.tm.Uploaded(id), start
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				up := d.tm.Uploaded(id)
				stats := seedStats{uploadBps: float64(up-lastUp) / now.Sub(lastAt).Seconds()}
				if size > 0 {
					stats.ratio = float64(up) / float64(size)
				}
				lastUp, lastAt = up, now
				d.seedMu.Lock()
				s.stats = stats
				d.seedMu.Unlock()

				ratioMet := opts.SeedRatio > 0 && stats.ratio >= opts.SeedRatio
				timeMet := opts.SeedTimeSec > 0 && now.Sub(start) >= time.Duration(opts.SeedTimeSec)*time.Second
				if ratioMet || timeMet {
					d.seedMu.Lock()
					delete(d.seeders, id)
					d.seedMu.Unlock()
					d.tm.Pause(id)
					d.st.UpdateStatus(context.Background(), id, store.StatusCompleted, "")
					return
				}
			}
		}
	}()
}

// stopSeeding ends job id's seeding early, if it is seeding, leaving it
// completed. It reports whether there was anything to stop.
func (d *Daemon) stopSeeding(id string) bool {
	d.seedMu.Lock()
	s, ok := d.seeders[id]
	delete(d.seeders, id)
	d.seedMu.Unlock()
	if !ok {
		return false
	}
	s.cancel()
	<-s.done
	d.tm.Pause(id)
	d.st.UpdateStatus(context.Background(), id, store.StatusCompleted, "")
	return true
}

// stopAllSeeding cancels every seeder at shutdown. Their jobs stay
// "seeding" in the store; resumeInterruptedJobs settles them as
// completed on the next start.
func (d *Daemon) stopAllSeeding() {
	d.seedMu.Lock()
	seeders := d.seeders
	d.seeders = nil
	d.seedMu.Unlock()
	for _, s := range seeders {
		s.cancel()
		<-s.done
	}
}

// withSeedStats fills in a seeding job's live upload speed and ratio.
func (d *Daemon) withSeedStats(v *JobView) *JobView {
	if v == nil || v.Status != store.StatusSeeding {
		return v
	}
	d.seedMu.Lock()
	if s, ok := d.seeders[v.ID]; ok {
		v.UploadBps, v.Ratio = s.stats.uploadBps, s.stats.ratio
	}
	d.seedMu.Unlock()
	return v
}
