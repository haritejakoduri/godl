package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/time/rate"

	"godl/internal/connections"
	"godl/internal/ratelimit"
	"godl/internal/store"
	"godl/internal/webdav"
)

// webdavDownloadConcurrency bounds how many files a folder job downloads
// at once — matches godl url's default chunk concurrency (-c 4), so a
// folder of many small-to-medium files moves roughly as fast as a
// single large one split into that many chunks, instead of paying for
// each file's round-trip and transfer serially.
const webdavDownloadConcurrency = 4

// SplitWebDAVSource parses the "<connection-name>:<remote-path>" form
// JoinWebDAVSource builds.
func SplitWebDAVSource(source string) (connName, remotePath string, ok bool) {
	i := strings.Index(source, ":")
	if i < 0 {
		return "", "", false
	}
	connName, remotePath = source[:i], source[i+1:]
	if connName == "" || remotePath == "" {
		return "", "", false
	}
	if !strings.HasPrefix(remotePath, "/") {
		remotePath = "/" + remotePath
	}
	return connName, remotePath, true
}

// JoinWebDAVSource builds a store.Job Source string for a WebDAV job —
// the encoding SplitWebDAVSource parses back apart. Used by cmd/webdav.go
// and cmd/webdavbrowse.go so both CLI/TUI entry points construct it the
// same way instead of each hand-rolling connName+":"+remotePath.
func JoinWebDAVSource(connName, remotePath string) string {
	return connName + ":" + remotePath
}

func (d *Daemon) startWebDAV(j *store.Job) {
	// One limiter instance shared by every file this job downloads
	// concurrently, so the job's own cap isn't multiplied by how many
	// files are in flight. globalLimiter is the Settings tab's shared
	// cap — see Daemon.globalLimiter.
	limiter := ratelimit.NewLimiter(j.LimitRate)
	globalLimiter := d.cachedGlobalLimiter()

	d.launch(j, func(ctx context.Context, rt *runtime) {
		connName, remotePath, ok := SplitWebDAVSource(j.Source)
		if !ok {
			d.finishJob(j.ID, j.BytesDone, false, fmt.Errorf("invalid webdav job source %q", j.Source))
			return
		}
		conn, err := connections.Get(connName)
		if err != nil {
			d.finishJob(j.ID, j.BytesDone, false, err)
			return
		}
		client, err := webdav.New(conn.URL, conn.Username, conn.Password, conn.Insecure)
		if err != nil {
			d.finishJob(j.ID, j.BytesDone, false, err)
			return
		}

		root, err := client.Stat(ctx, remotePath)
		if err != nil {
			d.finishJob(j.ID, j.BytesDone, false, fmt.Errorf("stat %s: %w", remotePath, err))
			return
		}

		var files []webdav.Entry
		if root.IsDir {
			files, err = client.Walk(ctx, remotePath)
		} else {
			files = []webdav.Entry{root}
		}
		if err != nil {
			if ctx.Err() != nil {
				d.finishJob(j.ID, j.BytesDone, false, context.Canceled)
				return
			}
			d.finishJob(j.ID, j.BytesDone, false, err)
			return
		}

		var total int64
		for _, f := range files {
			if f.Size > 0 {
				total += f.Size
			}
		}
		if total == 0 {
			total = -1
		}

		var cumulative atomic.Int64
		pending := d.pendingWebDAVFiles(j, files, remotePath, root.IsDir, &cumulative)
		d.reportProgress(j.ID, cumulative.Load(), total, nil)

		firstErr := d.downloadWebDAVFiles(ctx, rt, j, webdavDownload{
			client:        client,
			files:         pending,
			remotePath:    remotePath,
			rootIsDir:     root.IsDir,
			total:         total,
			limiter:       limiter,
			globalLimiter: globalLimiter,
			cumulative:    &cumulative,
		})

		final := cumulative.Load()
		switch {
		case firstErr != nil && (errors.Is(firstErr, context.Canceled) || ctx.Err() != nil):
			d.finishJob(j.ID, final, false, context.Canceled)
		case firstErr != nil:
			d.finishJob(j.ID, final, false, firstErr)
		case ctx.Err() != nil:
			d.finishJob(j.ID, final, false, context.Canceled)
		default:
			d.finishJob(j.ID, final, true, nil)
		}
	})
}

// pendingWebDAVFiles drops the files a previous run already finished,
// adding their sizes to cumulative so progress doesn't restart at zero.
// A file recorded as done but missing from disk (deleted between pause
// and resume) is downloaded again rather than silently counted.
//
// Done up front and sequentially: it's a stat per file, not a request,
// so it shouldn't compete for a download slot.
func (d *Daemon) pendingWebDAVFiles(j *store.Job, files []webdav.Entry, remotePath string, rootIsDir bool, cumulative *atomic.Int64) []webdav.Entry {
	alreadyDone := map[string]bool{}
	for _, p := range j.ResolvedPaths {
		alreadyDone[p] = true
	}
	pending := make([]webdav.Entry, 0, len(files))
	for _, f := range files {
		localPath := webdavLocalPath(j.Output, remotePath, f.Path, rootIsDir)
		if alreadyDone[localPath] {
			if fi, err := os.Stat(localPath); err == nil {
				cumulative.Add(fi.Size())
				continue
			}
		}
		pending = append(pending, f)
	}
	return pending
}

// webdavDownload is downloadWebDAVFiles' parameter list, which is long
// enough that positional arguments stop being readable.
type webdavDownload struct {
	client        *webdav.Client
	files         []webdav.Entry
	remotePath    string
	rootIsDir     bool
	total         int64
	limiter       *rate.Limiter
	globalLimiter *rate.Limiter
	cumulative    *atomic.Int64
}

// downloadWebDAVFiles fetches up to webdavDownloadConcurrency files at
// once — a folder of many files would otherwise pay for each one's
// round-trip serially — and returns the first error, if any. The first
// failure cancels the job so its siblings stop too.
func (d *Daemon) downloadWebDAVFiles(ctx context.Context, rt *runtime, j *store.Job, dl webdavDownload) error {
	sem := make(chan struct{}, webdavDownloadConcurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for _, f := range dl.files {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			localPath := webdavLocalPath(j.Output, dl.remotePath, f.Path, dl.rootIsDir)
			var lastDone int64
			written, derr := dl.client.Download(ctx, f.Path, localPath, dl.limiter, dl.globalLimiter, func(done, _ int64) {
				newCum := dl.cumulative.Add(done - lastDone)
				lastDone = done
				d.reportProgress(j.ID, newCum, dl.total, nil)
			})
			if derr != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("downloading %s: %w", f.Path, derr)
					rt.cancel() // stop this job's other in-flight downloads too
				}
				mu.Unlock()
				return
			}
			if delta := written - lastDone; delta != 0 {
				dl.cumulative.Add(delta)
			}
			d.st.AppendResolvedPath(context.Background(), j.ID, localPath)
		}()
	}
	wg.Wait()
	return firstErr
}

// webdavLocalPath maps a remote file (found under root, itself relative
// to the connection's base URL) to its destination on disk under output.
// For a single-file job (root itself is the file), it's just
// output/<basename>; for a folder job, the folder's own name plus its
// directory structure is preserved under output — downloading ".../Photos"
// lands at output/Photos/..., not dumped straight into output with the
// "Photos" name itself lost (which would also risk one folder's files
// silently overwriting another's if two folders share a subfolder name,
// e.g. two different albums each containing "vacation/"). The
// connection's own root ("/") has no name of its own to preserve, so
// downloading it still flattens directly under output, same as before.
func webdavLocalPath(output, root, filePath string, rootIsDir bool) string {
	if !rootIsDir {
		return filepath.Join(output, path.Base(filePath))
	}
	trimmedRoot := strings.TrimSuffix(root, "/")
	rel := strings.TrimPrefix(filePath, trimmedRoot)
	rel = strings.TrimPrefix(rel, "/")
	rootBase := ""
	if trimmedRoot != "" {
		rootBase = path.Base(trimmedRoot)
	}
	joined := filepath.Join(output, rootBase, filepath.FromSlash(rel))

	// Defense in depth: filePath comes straight from the WebDAV
	// server's PROPFIND response. A malicious or compromised server
	// (or a MITM'd connection using --insecure) could report an entry
	// path containing ".." segments, which filepath.Join above would
	// otherwise happily resolve to somewhere outside output. Refuse to
	// write outside the destination directory the user chose; fall
	// back to the file's own basename directly under output instead.
	outAbs, errOut := filepath.Abs(output)
	joinedAbs, errJoined := filepath.Abs(joined)
	if errOut != nil || errJoined != nil ||
		(joinedAbs != outAbs && !strings.HasPrefix(joinedAbs, outAbs+string(filepath.Separator))) {
		return filepath.Join(output, path.Base(filePath))
	}
	return joined
}
