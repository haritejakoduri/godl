package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"godl/internal/connections"
	"godl/internal/store"
	"godl/internal/webdav"
)

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

		var cumulative int64
		d.reportProgress(j.ID, cumulative, total)

		for _, f := range files {
			if ctx.Err() != nil {
				d.finishJob(j.ID, cumulative, false, context.Canceled)
				return
			}

			localPath := webdavLocalPath(j.Output, remotePath, f.Path, root.IsDir)

			if alreadyDone[localPath] {
				if fi, serr := os.Stat(localPath); serr == nil {
					cumulative += fi.Size()
					d.reportProgress(j.ID, cumulative, total)
					continue
				}
				// Recorded as already downloaded, but the local file is
				// gone (deleted by the user, or by something else,
				// between pause and resume) — fall through and download
				// it again rather than silently treating a missing file
				// as done.
			}

			base := cumulative
			written, derr := client.Download(ctx, f.Path, localPath, func(done, _ int64) {
				d.reportProgress(j.ID, base+done, total)
			})
			cumulative = base + written
			if derr != nil {
				if errors.Is(derr, context.Canceled) || ctx.Err() != nil {
					d.finishJob(j.ID, cumulative, false, context.Canceled)
					return
				}
				d.finishJob(j.ID, cumulative, false, fmt.Errorf("downloading %s: %w", f.Path, derr))
				return
			}
			d.st.AppendResolvedPath(context.Background(), j.ID, localPath)
		}

		d.finishJob(j.ID, cumulative, true, nil)
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
