package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"

	"godl/internal/downloader"
	"godl/internal/format"
	"godl/internal/ratelimit"
	"godl/internal/store"
	"godl/internal/torbox"
	"godl/internal/torrentmgr"
)

// Variables so tests can shorten them.
var (
	// torboxPoll is how often a torrent TorBox is still fetching is
	// checked on — one cheap API call; TorBox allows 300 a minute.
	torboxPoll = 4 * time.Second
	// torboxQueuePoll is the slower check for a torrent waiting in
	// TorBox's queue, which needs the whole list.
	torboxQueuePoll = 20 * time.Second
)

const (
	// torboxMaxFailures is how many API calls in a row may fail (network,
	// TorBox busy) before the job gives up.
	torboxMaxFailures = 15
	// Files fetched at once, and connections per file. TorBox serves
	// from a CDN that takes ranged requests, so a file splits like any
	// godl url download.
	torboxFileConcurrency = 2
	torboxChunks          = 8
	// torboxMetaTimeout bounds asking peers for a magnet's file order,
	// needed only when files were chosen by number (see torboxMapIndices).
	torboxMetaTimeout = 30 * time.Second
)

// torboxFile is one file of a TorBox torrent job, numbered the way the
// torrent itself numbers it, so --files means the same either way.
type torboxFile struct {
	Index    int // 0-based
	ID       int64
	Path     string // as --list-files shows it: no torrent folder
	Local    string // where it's saved
	Length   int64
	Selected bool
	done     atomic.Int64
}

// resolveVia settles how a new torrent is fetched: TorBox or godl's own
// client, following the TorBox default setting when the request leaves
// it open. It stores the answer, so a later settings change doesn't
// switch a torrent half way through.
func (d *Daemon) resolveVia(opts store.JobOptions) (store.JobOptions, error) {
	s := d.cachedSettings()
	switch opts.Via {
	case store.ViaTorBox:
		if !s.TorBoxReady() {
			return opts, fmt.Errorf("TorBox isn't set up — add your TorBox API key in Settings first")
		}
	case store.ViaP2P:
		opts.Via = ""
	case "":
		if s.TorBoxDefault && s.TorBoxReady() {
			opts.Via = store.ViaTorBox
		}
	default:
		return opts, fmt.Errorf("unknown way to fetch a torrent %q (want torbox or p2p)", opts.Via)
	}
	return opts, nil
}

func (rt *runtime) setPhase(p string) {
	rt.mu.Lock()
	rt.phase = p
	rt.mu.Unlock()
}

// startTorBox runs a torrent job through TorBox: add it there (or pick
// up the one a previous run added), wait while TorBox fetches it, then
// download the chosen files from TorBox over HTTPS.
func (d *Daemon) startTorBox(j *store.Job) {
	limiter := ratelimit.NewLimiter(j.LimitRate)
	globalLimiter := d.cachedGlobalLimiter()

	d.launch(j, func(ctx context.Context, rt *runtime) {
		var final int64 = j.BytesDone
		stop := func(err error) {
			if ctx.Err() != nil {
				err = context.Canceled
			}
			d.finishJob(j.ID, final, false, err)
		}

		settings := d.cachedSettings()
		if !settings.TorBoxReady() {
			stop(fmt.Errorf("TorBox isn't set up — add your TorBox API key in Settings, or retry this torrent without TorBox"))
			return
		}
		sel, err := torrentmgr.ParseSelection(j.Options.TorrentFiles)
		if err != nil {
			stop(err)
			return
		}
		client := torbox.New(settings.TorBoxAPIKey)

		t, err := d.torboxWaitReady(ctx, rt, client, j)
		if err != nil {
			stop(err)
			return
		}

		files := d.torboxFileList(t, j, sel)
		var total int64
		chosen := 0
		for _, f := range files {
			if f.Selected {
				total += f.Length
				chosen++
			}
		}
		if chosen == 0 {
			stop(fmt.Errorf("--files %q matched none of the torrent's %d file(s)", j.Options.TorrentFiles, len(files)))
			return
		}
		rt.mu.Lock()
		rt.torboxFiles = files
		rt.mu.Unlock()
		if job, gerr := d.st.GetJob(context.Background(), j.ID); gerr == nil {
			job.InfoHash = strings.ToLower(t.Hash)
			if len(files) > 0 {
				job.ResolvedPaths = []string{torboxTopName(t, files)}
			}
			d.st.UpdateJob(context.Background(), job)
		}

		var cumulative atomic.Int64
		var pending []*torboxFile
		for _, f := range files {
			if !f.Selected {
				continue
			}
			if torboxFileComplete(f) {
				f.done.Store(f.Length)
				cumulative.Add(f.Length)
				continue
			}
			pending = append(pending, f)
		}
		rt.setPhase("from TorBox")
		d.reportProgress(j.ID, cumulative.Load(), total, nil)

		err = d.torboxDownload(ctx, client, t.ID, pending, total, &cumulative, j, limiter, globalLimiter)
		final = cumulative.Load()
		if err != nil {
			stop(err)
			return
		}
		if ctx.Err() != nil {
			stop(context.Canceled)
			return
		}

		if !settings.TorBoxKeep {
			// Free the TorBox slot; the files are here now. Best effort —
			// a failure only leaves the torrent in the account.
			dctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if client.Delete(dctx, t.ID) == nil {
				if job, gerr := d.st.GetJob(context.Background(), j.ID); gerr == nil {
					job.Options.TorBoxID = 0
					d.st.UpdateJob(context.Background(), job)
				}
			}
			cancel()
		}
		d.finishJob(j.ID, final, true, nil)
	})
}

// torboxWaitReady adds the job's torrent to TorBox if this is its first
// run, then polls until TorBox has every file ready, showing TorBox's
// own progress meanwhile.
func (d *Daemon) torboxWaitReady(ctx context.Context, rt *runtime, client *torbox.Client, j *store.Job) (*torbox.Torrent, error) {
	id := j.Options.TorBoxID
	failures := 0
	readded := false
	retryable := func(err error) error {
		if torbox.Unauthorized(err) {
			return fmt.Errorf("%w — check the TorBox API key in Settings", err)
		}
		failures++
		if failures >= torboxMaxFailures {
			return err
		}
		return nil
	}
	sleep := func(dur time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(dur):
			return true
		}
	}

	for {
		if id == 0 {
			rt.setPhase("TorBox: adding the torrent")
			created, err := client.Add(ctx, j.Source)
			if err != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				var apiErr *torbox.APIError
				if errors.As(err, &apiErr) && apiErr.Status < 500 && apiErr.Status != 429 {
					return nil, err // refused outright: a bad link, the plan's limit, ...
				}
				if ferr := retryable(err); ferr != nil {
					return nil, ferr
				}
				if !sleep(torboxPoll) {
					return nil, ctx.Err()
				}
				continue
			}
			id = created.TorrentID
			for id == 0 {
				// Every active slot of the plan is in use: TorBox queued it,
				// and it gets an ID when it starts.
				rt.setPhase("TorBox: waiting in your TorBox queue for a free slot")
				if !sleep(torboxQueuePoll) {
					return nil, ctx.Err()
				}
				if created.Hash == "" {
					return nil, fmt.Errorf("TorBox queued the torrent but didn't say which it was")
				}
				found, err := client.FindByHash(ctx, created.Hash)
				if err != nil {
					if ferr := retryable(err); ferr != nil {
						return nil, ferr
					}
					continue
				}
				if found != nil {
					id = found.ID
				}
			}
			if job, gerr := d.st.GetJob(context.Background(), j.ID); gerr == nil {
				job.Options.TorBoxID = id
				d.st.UpdateJob(context.Background(), job)
			}
			j.Options.TorBoxID = id
		}

		t, err := client.Torrent(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			var apiErr *torbox.APIError
			if errors.As(err, &apiErr) && apiErr.Status == 404 && !readded {
				// Deleted from the account since the last run: add it again.
				id, readded = 0, true
				continue
			}
			if ferr := retryable(err); ferr != nil {
				return nil, ferr
			}
			if !sleep(torboxPoll) {
				return nil, ctx.Err()
			}
			continue
		}
		failures = 0
		if t.Failed() {
			return nil, fmt.Errorf("TorBox couldn't download this torrent (%s)", t.DownloadState)
		}
		if t.Ready() && len(t.Files) > 0 {
			return t, nil
		}
		rt.setPhase(torboxPhase(t))
		if !sleep(torboxPoll) {
			return nil, ctx.Err()
		}
	}
}

// torboxPhase describes what TorBox is doing with a torrent, for the
// job list while nothing has reached this computer yet.
func torboxPhase(t *torbox.Torrent) string {
	if t.DownloadFinished {
		return "TorBox: finishing up"
	}
	state := strings.ToLower(t.DownloadState)
	switch {
	case strings.Contains(state, "meta"):
		return "TorBox: getting the torrent's details from peers"
	case strings.Contains(state, "stalled"):
		return fmt.Sprintf("TorBox: %.0f%% · stalled, waiting for peers", t.Progress*100)
	case strings.Contains(state, "queued"):
		return "TorBox: queued"
	case strings.Contains(state, "check"):
		return "TorBox: checking"
	}
	parts := []string{fmt.Sprintf("TorBox: downloading %.0f%%", t.Progress*100)}
	if t.DownloadSpeed > 0 {
		parts = append(parts, format.Bytes(int64(t.DownloadSpeed))+"/s")
	}
	if t.Seeds > 0 {
		parts = append(parts, fmt.Sprintf("%d seeds", t.Seeds))
	}
	return strings.Join(parts, " · ")
}

// torboxFileList turns TorBox's file list into the job's, numbered as
// the torrent numbers its files and marked with what's chosen.
func (d *Daemon) torboxFileList(t *torbox.Torrent, j *store.Job, sel *torrentmgr.Selection) []*torboxFile {
	tf := append([]torbox.File(nil), t.Files...)
	// TorBox gives files IDs in the torrent's own order.
	sort.Slice(tf, func(a, b int) bool { return tf[a].ID < tf[b].ID })
	files := make([]*torboxFile, len(tf))
	for i, f := range tf {
		files[i] = &torboxFile{Index: i, ID: f.ID, Path: torboxDisplayPath(t.Name, f), Local: torboxLocalPath(j.Output, f), Length: f.Size}
	}
	if sel.UsesIndices() && d.tm != nil {
		torboxMapIndices(d.tm, j.Source, files)
	}
	for _, f := range files {
		f.Selected = sel.Match(f.Index+1, f.Path)
	}
	return files
}

// torboxMapIndices renumbers files by the torrent's metadata, matching
// on path, in case TorBox's order ever differs from it: files chosen by
// number in the picker were numbered from that metadata. Leaves the
// numbering alone unless every file matches.
func torboxMapIndices(tm interface {
	ListFiles(string, time.Duration) (string, []torrentmgr.FileInfo, error)
}, source string, files []*torboxFile) {
	_, meta, err := tm.ListFiles(source, torboxMetaTimeout)
	if err != nil || len(meta) != len(files) {
		return
	}
	byPath := make(map[string]int, len(meta))
	for _, m := range meta {
		byPath[m.Path] = m.Index
	}
	idx := make([]int, len(files))
	for i, f := range files {
		k, ok := byPath[f.Path]
		if !ok {
			return
		}
		idx[i] = k
	}
	for i, f := range files {
		f.Index = idx[i]
	}
	sort.Slice(files, func(a, b int) bool { return files[a].Index < files[b].Index })
}

// torboxDisplayPath is a file's path within the torrent, as godl's own
// client shows it: without the torrent's folder.
func torboxDisplayPath(torrentName string, f torbox.File) string {
	name := strings.TrimPrefix(path.Clean("/"+f.Name), "/")
	if rest, ok := strings.CutPrefix(name, torrentName+"/"); ok && torrentName != "" {
		return rest
	}
	return name
}

// torboxLocalPath is where a file lands: under output, keeping the
// torrent's folder, exactly where godl's own client would put it.
// TorBox supplies the names, so a path trying to climb out of output
// is cut down to its base name.
func torboxLocalPath(output string, f torbox.File) string {
	rel := filepath.FromSlash(strings.TrimPrefix(path.Clean("/"+f.Name), "/"))
	if rel == "" || rel == "." {
		rel = filepath.Base(f.ShortName)
	}
	joined := filepath.Join(output, rel)
	outAbs, err1 := filepath.Abs(output)
	abs, err2 := filepath.Abs(joined)
	if err1 != nil || err2 != nil || (abs != outAbs && !strings.HasPrefix(abs, outAbs+string(filepath.Separator))) {
		return filepath.Join(output, filepath.Base(f.Name))
	}
	return joined
}

// torboxTopName is what the torrent adds directly under output — its
// folder, or its one file — recorded so "remove --purge" deletes just
// that, as for a torrent fetched by godl's own client.
func torboxTopName(t *torbox.Torrent, files []*torboxFile) string {
	first := strings.TrimPrefix(path.Clean("/"+t.Files[0].Name), "/")
	if top, _, ok := strings.Cut(first, "/"); ok {
		return top
	}
	return filepath.Base(files[0].Local)
}

// torboxFileComplete: a previous run finished this file. The chunked
// downloader writes a sidecar while working and preallocates the file,
// so the size alone doesn't say it's done; the sidecar's absence does.
func torboxFileComplete(f *torboxFile) bool {
	fi, err := os.Stat(f.Local)
	if err != nil || fi.Size() != f.Length {
		return false
	}
	_, err = os.Stat(f.Local + ".godl-progress.json")
	return os.IsNotExist(err)
}

// torboxDownload fetches files from TorBox, a couple at a time, each in
// several ranged pieces. The first failure stops the rest.
func (d *Daemon) torboxDownload(ctx context.Context, client *torbox.Client, torrentID int64, files []*torboxFile, total int64,
	cumulative *atomic.Int64, j *store.Job, limiter, globalLimiter *rate.Limiter) error {
	dctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sem := make(chan struct{}, torboxFileConcurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for _, f := range files {
		if dctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			err := d.torboxFetchFile(dctx, client, torrentID, f, total, cumulative, j, limiter, globalLimiter)
			if err != nil && dctx.Err() == nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("downloading %s from TorBox: %w", f.Path, err)
					cancel()
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return firstErr
}

// torboxFetchFile downloads one file. Its link is asked for fresh each
// attempt: links expire after a few hours, and a second attempt
// resumes from the downloader's sidecar rather than starting over.
func (d *Daemon) torboxFetchFile(ctx context.Context, client *torbox.Client, torrentID int64, f *torboxFile, total int64,
	cumulative *atomic.Int64, j *store.Job, limiter, globalLimiter *rate.Limiter) error {
	if err := os.MkdirAll(filepath.Dir(f.Local), 0o755); err != nil {
		return err
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 3 * time.Second):
			}
		}
		link, err := client.Link(ctx, torrentID, f.ID)
		if err != nil {
			if torbox.Unauthorized(err) || ctx.Err() != nil {
				return err
			}
			lastErr = err
			continue
		}
		res, err := downloader.Run(ctx, downloader.Options{
			URL:           link,
			OutputPath:    f.Local,
			Concurrency:   torboxChunks,
			Limiter:       limiter,
			GlobalLimiter: globalLimiter,
			Progress: func(done, _ int64) {
				prev := f.done.Swap(done)
				d.reportProgress(j.ID, cumulative.Add(done-prev), total, nil)
			},
		})
		if delta := res.BytesDone - f.done.Load(); res.BytesDone > 0 && delta != 0 {
			f.done.Store(res.BytesDone)
			cumulative.Add(delta)
		}
		if err == nil && res.Completed {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			err = fmt.Errorf("the download ended early")
		}
		lastErr = err
	}
	return lastErr
}

// torboxRuntimeFiles lists a running TorBox job's files with progress,
// or nil before TorBox has them ready.
func (d *Daemon) torboxRuntimeFiles(id string) []TorrentFile {
	rt := d.getRuntime(id)
	if rt == nil {
		return nil
	}
	rt.mu.Lock()
	files := rt.torboxFiles
	rt.mu.Unlock()
	if files == nil {
		return nil
	}
	out := make([]TorrentFile, len(files))
	for i, f := range files {
		out[i] = TorrentFile{Index: f.Index, Path: f.Path, Length: f.Length, Done: f.done.Load(), Skipped: !f.Selected}
	}
	return out
}

// torboxChoice lists a TorBox job's files for choosing among: from the
// running job, or from TorBox for one that's paused or finished. ok is
// false when TorBox can't answer (the torrent isn't there yet), and the
// caller falls back to the torrent's own metadata.
func (d *Daemon) torboxChoice(ctx context.Context, job *store.Job) (name string, files []TorrentFile, ok bool) {
	if tf := d.torboxRuntimeFiles(job.ID); tf != nil {
		if len(job.ResolvedPaths) > 0 {
			name = job.ResolvedPaths[0]
		}
		return name, tf, true
	}
	s := d.cachedSettings()
	if job.Options.TorBoxID == 0 || !s.TorBoxReady() {
		return "", nil, false
	}
	t, err := torbox.New(s.TorBoxAPIKey).Torrent(ctx, job.Options.TorBoxID)
	if err != nil || len(t.Files) == 0 {
		return "", nil, false
	}
	sel, err := torrentmgr.ParseSelection(job.Options.TorrentFiles)
	if err != nil {
		return "", nil, false
	}
	for _, f := range d.torboxFileList(t, job, sel) {
		files = append(files, TorrentFile{Index: f.Index, Path: f.Path, Length: f.Length, Skipped: !f.Selected})
	}
	return t.Name, files, true
}

// forgetTorBox deletes a job's torrent from the TorBox account, best
// effort, unless the user keeps them there. wait: before returning —
// for a retry, which adds the torrent again right after and mustn't
// have that copy deleted under it; otherwise in the background.
func (d *Daemon) forgetTorBox(job *store.Job, wait bool) {
	s := d.cachedSettings()
	if !job.Options.TorBox() || job.Options.TorBoxID == 0 || !s.TorBoxReady() || s.TorBoxKeep {
		return
	}
	id := job.Options.TorBoxID
	del := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		torbox.New(s.TorBoxAPIKey).Delete(ctx, id)
	}
	if wait {
		del()
	} else {
		go del()
	}
}
