package daemon

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"godl/internal/store"
)

// streamServer serves running torrent jobs' files over loopback HTTP so
// a media player can play them while they download. Each request gets
// its own anacrolix reader, which raises the priority of the pieces just
// ahead of wherever the player is reading (and seeking to), so playback
// starts after a few MB instead of after the whole file.
//
// It binds 127.0.0.1 only, and every URL carries a random per-daemon
// token, so another local user can't pull files through it by guessing
// job IDs.
type streamServer struct {
	srv   *http.Server
	addr  string
	token string
}

// streamableExts are the extensions picked by default when a torrent
// holds several files, so "o" plays the episode rather than the .nfo.
var streamableExts = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".webm": true, ".avi": true,
	".mov": true, ".wmv": true, ".flv": true, ".ts": true, ".m2ts": true,
	".mpg": true, ".mpeg": true, ".ogv": true,
	".mp3": true, ".flac": true, ".m4a": true, ".aac": true, ".ogg": true,
	".opus": true, ".wav": true,
}

// streamTorrent returns a URL for streaming one file of running torrent
// job jobID (fileIndex < 0 picks the largest media file among the job's
// selected files), plus the job's file list.
func (d *Daemon) streamTorrent(jobID string, fileIndex int) (string, []TorrentFile, error) {
	job, err := d.st.GetJob(context.Background(), jobID)
	if err != nil {
		return "", nil, fmt.Errorf("job %s not found", jobID)
	}
	if job.Type != store.JobTorrent {
		return "", nil, fmt.Errorf("job %s is a %s job, not a torrent", jobID, job.Type)
	}
	if job.Options.TorBox() {
		return "", nil, fmt.Errorf("this torrent comes through TorBox; it can be played once it has downloaded")
	}
	infos, selected := d.tm.Files(jobID)
	if infos == nil {
		switch job.Status {
		case store.StatusActive, store.StatusQueued:
			return "", nil, fmt.Errorf("job %s is still fetching the torrent's metadata — try again in a moment", jobID)
		default:
			return "", nil, fmt.Errorf("job %s is %s; resume it to stream it", jobID, job.Status)
		}
	}
	files := make([]TorrentFile, len(infos))
	for i, f := range infos {
		files[i] = TorrentFile{Index: f.Index, Path: f.Path, Length: f.Length, Done: f.Done}
	}

	if fileIndex < 0 {
		fileIndex = pickStreamFile(files, selected)
	}
	if fileIndex < 0 || fileIndex >= len(files) {
		return "", files, fmt.Errorf("job %s has no file #%d (it has %d)", jobID, fileIndex+1, len(files))
	}

	srv, err := d.ensureStreamServer()
	if err != nil {
		return "", files, err
	}
	u := fmt.Sprintf("http://%s/%s/%s/%d/%s", srv.addr, srv.token, url.PathEscape(jobID), fileIndex,
		url.PathEscape(path.Base(files[fileIndex].Path)))
	return u, files, nil
}

// pickStreamFile returns the largest selected media file, falling back
// to the largest selected file of any kind.
func pickStreamFile(files []TorrentFile, selected []bool) int {
	best, bestMedia := -1, -1
	for i, f := range files {
		if i < len(selected) && !selected[i] {
			continue
		}
		if best < 0 || f.Length > files[best].Length {
			best = i
		}
		if streamableExts[strings.ToLower(path.Ext(f.Path))] && (bestMedia < 0 || f.Length > files[bestMedia].Length) {
			bestMedia = i
		}
	}
	if bestMedia >= 0 {
		return bestMedia
	}
	return best
}

func (d *Daemon) ensureStreamServer() (*streamServer, error) {
	d.streamMu.Lock()
	defer d.streamMu.Unlock()
	if d.streamSrv != nil {
		return d.streamSrv, nil
	}
	var tok [16]byte
	if _, err := rand.Read(tok[:]); err != nil {
		return nil, err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("starting the torrent stream server: %w", err)
	}
	s := &streamServer{addr: l.Addr().String(), token: hex.EncodeToString(tok[:])}
	s.srv = &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { d.serveStream(s, w, r) }),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := s.srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("torrent stream server: %v", err)
		}
	}()
	d.streamSrv = s
	return s, nil
}

func (d *Daemon) closeStreamServer() {
	d.streamMu.Lock()
	s := d.streamSrv
	d.streamSrv = nil
	d.streamMu.Unlock()
	if s != nil {
		s.srv.Close()
	}
}

// serveStream handles /<token>/<job-id>/<file-index>/<name>. The name is
// only there so players can guess the format from the URL.
func (d *Daemon) serveStream(s *streamServer, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 4)
	if len(parts) < 3 || subtle.ConstantTimeCompare([]byte(parts[0]), []byte(s.token)) != 1 {
		http.NotFound(w, r)
		return
	}
	idx, err := strconv.Atoi(parts[2])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, ok := d.tm.File(parts[1], idx)
	if !ok {
		http.Error(w, "that torrent job isn't running (paused, finished or removed)", http.StatusNotFound)
		return
	}

	reader := f.NewReader()
	defer reader.Close()
	// Stop waiting for pieces once the player hangs up or seeks away.
	reader.SetContext(r.Context())
	// Hand data over as soon as it's downloaded, not once its whole
	// piece has been hash-checked — lower startup latency for playback.
	reader.SetResponsive()
	reader.SetReadahead(streamReadahead(f.Length()))

	name := path.Base(f.DisplayPath())
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeContent(w, r, name, time.Time{}, reader)
}

// streamReadahead is how far past the play position to prioritize: 1%
// of the file, within 8-64 MiB — seconds of video at typical bitrates,
// without starving the rest of the download.
func streamReadahead(length int64) int64 {
	const lo, hi = 8 << 20, 64 << 20
	return min(max(length/100, lo), hi)
}
