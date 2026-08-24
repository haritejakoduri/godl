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
	"time"

	"godl/internal/connections"
	"godl/internal/store"
	"godl/internal/webdav"
)

// webdavDownloadConcurrency bounds how many files a folder job downloads
// at once — matches godl url's default chunk concurrency (-c 4), so a
// folder of many small-to-medium files moves roughly as fast as a
// single large one split into that many chunks, instead of paying for
// each file's round-trip and transfer serially.
const webdavDownloadConcurrency = 4

// splitWebDAVSource parses the "<connection-name>:<remote-path>" form
// createWebDAVSource builds — see cmd/webdav.go.
func splitWebDAVSource(source string) (connName, remotePath string, ok bool) {
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

func (d *Daemon) startWebDAV(j *store.Job) {
	ctx, cancel := context.WithCancel(context.Background())
	rt := &runtime{cancel: cancel, done: make(chan struct{}), lastTime: time.Now(), bytesDone: j.BytesDone, bytesTotal: j.BytesTotal}
	d.setRuntime(j.ID, rt)
	d.st.UpdateStatus(context.Background(), j.ID, store.StatusActive, "")

	go func() {
		defer close(rt.done)
		defer d.clearRuntime(j.ID)

		connName, remotePath, ok := splitWebDAVSource(j.Source)
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

		alreadyDone := map[string]bool{}
		for _, p := range j.ResolvedPaths {
			alreadyDone[p] = true
		}

		// cumulative is updated concurrently (each in-flight file's own
		// progress callback adds its delta), so every read/write of it
		// past this point goes through the atomic.
		var cumulative atomic.Int64

		// Skipping already-downloaded files is cheap (a stat, not a
		// request) and doesn't need to compete for the download
		// semaphore below, so it's done up front, sequentially.
		pending := make([]webdav.Entry, 0, len(files))
		for _, f := range files {
			localPath := webdavLocalPath(j.Output, remotePath, f.Path, root.IsDir)
			if alreadyDone[localPath] {
				if fi, serr := os.Stat(localPath); serr == nil {
					cumulative.Add(fi.Size())
					continue
				}
				// Recorded as already downloaded, but the local file is
				// gone (deleted by the user, or by something else,
				// between pause and resume) — fall through and download
				// it again rather than silently treating a missing file
				// as done.
			}
			pending = append(pending, f)
		}
		d.reportProgress(j.ID, cumulative.Load(), total)

		// Download up to webdavDownloadConcurrency files at once —
		// otherwise a folder of many files pays for each one's
		// round-trip and transfer serially, the same problem godl url's
		// chunked concurrency solves for a single large file.
		sem := make(chan struct{}, webdavDownloadConcurrency)
		var wg sync.WaitGroup
		var mu sync.Mutex
		var firstErr error

		for _, f := range pending {
			if ctx.Err() != nil {
				break
			}
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()

				localPath := webdavLocalPath(j.Output, remotePath, f.Path, root.IsDir)
				var lastDone int64
				written, derr := client.Download(ctx, f.Path, localPath, func(done, _ int64) {
					newCum := cumulative.Add(done - lastDone)
					lastDone = done
					d.reportProgress(j.ID, newCum, total)
				})
				if derr != nil {
					mu.Lock()
					if firstErr == nil {
						firstErr = fmt.Errorf("downloading %s: %w", f.Path, derr)
						cancel() // stop the rest of this job's in-flight downloads too
					}
					mu.Unlock()
					return
				}
				if delta := written - lastDone; delta != 0 {
					cumulative.Add(delta)
				}
				d.st.AppendResolvedPath(context.Background(), j.ID, localPath)
			}()
		}
		wg.Wait()

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
	}()
}

// webdavLocalPath maps a remote file (found under root, itself relative
// to the connection's base URL) to its destination on disk under output.
// For a single-file job (root itself is the file), it's just
// output/<basename>; for a folder job, the folder's own directory
// structure is preserved under output.
func webdavLocalPath(output, root, filePath string, rootIsDir bool) string {
	if !rootIsDir {
		return filepath.Join(output, path.Base(filePath))
	}
	rel := strings.TrimPrefix(filePath, strings.TrimSuffix(root, "/"))
	rel = strings.TrimPrefix(rel, "/")
	joined := filepath.Join(output, filepath.FromSlash(rel))

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
