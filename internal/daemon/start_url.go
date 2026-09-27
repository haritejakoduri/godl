package daemon

import (
	"context"

	"godl/internal/downloader"
	"godl/internal/ratelimit"
	"godl/internal/reqhdr"
	"godl/internal/store"
)

func (d *Daemon) startURL(j *store.Job) {
	// Single-stream downloads (concurrency<=1) checkpoint their resume
	// offset on every progress tick. Chunked ones use a sidecar file
	// instead (see internal/downloader) and ignore ResumeOffset.
	single := j.Concurrency <= 1

	d.launch(j, func(ctx context.Context, rt *runtime) {
		// Parsed at start, not creation, so an updated cookies file is
		// picked up by a resume or retry.
		hdr, err := reqhdr.Build(j.Options.Headers, j.Options.CookiesFile)
		if err != nil {
			d.finishJob(j.ID, j.BytesDone, false, err)
			return
		}
		res, err := downloader.Run(ctx, downloader.Options{
			Headers:       hdr,
			URL:           j.Source,
			OutputPath:    j.Output,
			Concurrency:   j.Concurrency,
			StartOffset:   j.ResumeOffset,
			Limiter:       ratelimit.NewLimiter(j.LimitRate),
			GlobalLimiter: d.cachedGlobalLimiter(),
			Sha256:        j.Sha256,
			Progress: func(done, total int64) {
				// The resume offset rides along in the same write rather
				// than being a second UPDATE of the row just written.
				var resume *int64
				if single {
					resume = &done
				}
				d.reportProgress(j.ID, done, total, resume)
			},
		})
		d.finishJob(j.ID, res.BytesDone, res.Completed, err)
	})
}
