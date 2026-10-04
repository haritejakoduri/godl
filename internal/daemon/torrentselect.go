package daemon

import (
	"context"
	"fmt"
	"path/filepath"

	"godl/internal/store"
	"godl/internal/torrentmgr"
)

// torrentChoice lists every file of torrent job id, each marked with
// whether the job is set to download it. A running torrent answers from
// the client (with progress); otherwise the list comes from the
// torrent's metadata — read from the .torrent file, or for a magnet
// fetched from peers, which can take a while.
func (d *Daemon) torrentChoice(id string) (name string, files []TorrentFile, err error) {
	job, err := d.st.GetJob(context.Background(), id)
	if err != nil {
		return "", nil, fmt.Errorf("job %s not found", id)
	}
	if job.Type != store.JobTorrent {
		return "", nil, fmt.Errorf("job %s is a %s job, not a torrent", id, job.Type)
	}
	if job.Options.TorBox() {
		if name, files, ok := d.torboxChoice(context.Background(), job); ok {
			return name, files, nil
		}
	}
	if infos, selected := d.tmFiles(id); infos != nil {
		for i, f := range infos {
			files = append(files, TorrentFile{Index: f.Index, Path: f.Path, Length: f.Length, Done: f.Done,
				Skipped: i < len(selected) && !selected[i]})
		}
		if len(job.ResolvedPaths) > 0 {
			name = filepath.Base(job.ResolvedPaths[0])
		}
		return name, files, nil
	}

	name, infos, err := d.tm.ListFiles(job.Source, torrentInfoTimeout)
	if err != nil {
		return "", nil, err
	}
	sel, err := torrentmgr.ParseSelection(job.Options.TorrentFiles)
	if err != nil {
		return "", nil, err
	}
	for _, f := range infos {
		files = append(files, TorrentFile{Index: f.Index, Path: f.Path, Length: f.Length,
			Skipped: sel != nil && !sel.Match(f.Index+1, f.Path)})
	}
	return name, files, nil
}

// selectFiles changes which of torrent job id's files are downloaded.
//
// A running torrent switches at once: newly chosen files start, dropped
// ones stop (what's already on disk of them stays). A paused, queued or
// failed one keeps the choice for when it runs. A finished one is
// started again, so files just added get downloaded — the client checks
// what's on disk first, so nothing already there is fetched twice.
func (d *Daemon) selectFiles(ctx context.Context, id, spec string) (*store.Job, error) {
	job, err := d.st.GetJob(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("job %s not found", id)
	}
	if job.Type != store.JobTorrent {
		return nil, fmt.Errorf("job %s is a %s job, not a torrent", id, job.Type)
	}
	sel, err := torrentmgr.ParseSelection(spec)
	if err != nil {
		return nil, err
	}

	if infos, _ := d.tmFiles(id); infos != nil {
		n, err := d.tm.Select(id, sel)
		if err == nil && n == 0 {
			err = fmt.Errorf("that choice leaves no files to download")
		}
		if err != nil {
			// Put the previous choice back rather than leave it half-applied.
			if old, perr := torrentmgr.ParseSelection(job.Options.TorrentFiles); perr == nil {
				d.tm.Select(id, old)
			}
			return nil, err
		}
	}

	// A running TorBox torrent takes a new choice by starting over:
	// files already here are kept and not fetched again, and newly
	// chosen ones join in.
	restart := job.Options.TorBox() && (job.Status == store.StatusActive || job.Status == store.StatusQueued)
	if restart {
		if tf := d.torboxRuntimeFiles(id); tf != nil {
			n := 0
			for _, f := range tf {
				if sel.Match(f.Index+1, f.Path) {
					n++
				}
			}
			if n == 0 {
				return nil, fmt.Errorf("that choice leaves no files to download")
			}
		}
		if _, err := d.pause(ctx, id); err != nil {
			return nil, err
		}
		if job, err = d.st.GetJob(ctx, id); err != nil {
			return nil, err
		}
		job.Status = store.StatusQueued
		job.ErrorMsg = ""
	}

	job.Options.TorrentFiles = spec
	finished := job.Status == store.StatusCompleted || job.Status == store.StatusSeeding
	if finished {
		d.stopSeeding(id)
		job.Status = store.StatusQueued
		job.ErrorMsg = ""
	}
	if err := d.st.UpdateJob(ctx, job); err != nil {
		return nil, err
	}
	if finished || restart {
		d.start(job)
	}
	return d.st.GetJob(ctx, id)
}

// tmFiles is d.tm.Files, tolerating a daemon built without a torrent
// client (as the package's tests build one).
func (d *Daemon) tmFiles(id string) ([]torrentmgr.FileInfo, []bool) {
	if d.tm == nil {
		return nil, nil
	}
	return d.tm.Files(id)
}
