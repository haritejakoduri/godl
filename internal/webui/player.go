package webui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"godl/internal/ffmpeg"
	"godl/internal/ytdlp"
)

// OpenRequest names what to play. The page never sends a file path —
// only a reference the server resolves itself, so the player can't be
// talked into reading an arbitrary file.
type OpenRequest struct {
	Kind string `json:"kind"` // "job" | "webdav" | "link"

	Job  string `json:"job,omitempty"`
	File int    `json:"file,omitempty"` // torrent jobs: 1-based file number, 0 = the main one

	Conn string `json:"conn,omitempty"`
	Path string `json:"path,omitempty"`

	Link    string `json:"link,omitempty"`
	Quality int    `json:"quality,omitempty"` // max height, 0 = no cap
}

// A Resolver turns a "job" or "webdav" OpenRequest into a Source.
type Resolver func(ctx context.Context, req OpenRequest) (Source, error)

const (
	// maxStreams caps concurrent ffmpeg processes. One viewer changing
	// language overlaps two for a moment; three leaves room for that
	// plus a second viewer without letting a stuck tab pile them up.
	maxStreams     = 3
	streamSlotWait = 3 * time.Second
	maxSessions    = 64
	// linkTTL is how long an "open in another app" link works.
	linkTTL = 12 * time.Hour
	// probeTimeout bounds reading a source's metadata. A magnet that
	// has no peers, or a dead server, shouldn't hang the page.
	probeTimeout = 60 * time.Second
)

// Player serves the in-page player: what a source contains, the stream
// itself, its subtitles, and links for playing it somewhere else.
type Player struct {
	resolve Resolver

	// Swappable so tests run without downloading the real tools.
	ffmpegDir func(context.Context) (string, error)
	ytdlpPath func(context.Context) (string, error)

	mu       sync.Mutex
	sessions map[string]*session
	links    map[string]*extLink

	slots chan struct{}

	relay relay
}

type session struct {
	src  Source
	info Info
	used time.Time

	// viaRelay is src with its URLs pointed at the relay, made on first
	// use (see Player.input).
	viaRelay *Source
}

type extLink struct {
	session string
	expires time.Time
}

func NewPlayer(resolve Resolver) *Player {
	return &Player{
		resolve:   resolve,
		ffmpegDir: func(ctx context.Context) (string, error) { return ffmpeg.Ensure(ctx, nil) },
		ytdlpPath: func(ctx context.Context) (string, error) { return ytdlp.Ensure(ctx, nil) },
		sessions:  map[string]*session{},
		links:     map[string]*extLink{},
		slots:     make(chan struct{}, maxStreams),
	}
}

// Routes registers the player's authenticated API on mux. ExternalLink
// is mounted separately, outside authentication.
func (p *Player) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/play/open", p.handleOpen)
	mux.HandleFunc("GET /api/play/{id}/stream", p.handleStream)
	mux.HandleFunc("GET /api/play/{id}/file", p.handleFile)
	mux.HandleFunc("GET /api/play/{id}/seek", p.handleSeek)
	mux.HandleFunc("GET /api/play/{id}/subs", p.handleSubs)
	mux.HandleFunc("POST /api/play/{id}/link", p.handleLink)
}

func (p *Player) tool(ctx context.Context, name string) (string, error) {
	dir, err := p.ffmpegDir(ctx)
	if err != nil {
		return "", fmt.Errorf("getting ffmpeg: %w", err)
	}
	return filepath.Join(dir, name), nil
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // the OS has no randomness; nothing sensible to do
	}
	return hex.EncodeToString(b[:])
}

func (p *Player) handleOpen(w http.ResponseWriter, r *http.Request) {
	var req OpenRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Fetched before the timeout starts: the first use of the player
	// downloads ffmpeg (and yt-dlp, for a link), which takes as long as
	// it takes and isn't the source's fault.
	if _, err := p.tool(r.Context(), "ffprobe"); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	var ytDlp string
	if req.Kind == "link" {
		var err error
		if ytDlp, err = p.ytdlpPath(r.Context()); err != nil {
			writeError(w, http.StatusBadGateway, fmt.Errorf("getting yt-dlp: %w", err))
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()

	var src Source
	var info Info
	var err error
	if req.Kind == "link" {
		src, info, err = openLink(ctx, ytDlp, req)
	} else {
		src, info, err = p.openFile(ctx, req)
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}

	id := randomID()
	p.mu.Lock()
	p.evictLocked()
	p.sessions[id] = &session{src: src, info: info, used: time.Now()}
	p.mu.Unlock()

	writeJSON(w, struct {
		ID string `json:"id"`
		Info
	}{id, info})
}

func openLink(ctx context.Context, ytDlp string, req OpenRequest) (Source, Info, error) {
	link := strings.TrimSpace(req.Link)
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return Source{}, Info{}, fmt.Errorf("that doesn't look like a web link")
	}
	return resolveLink(ctx, ytDlp, link, req.Quality)
}

func (p *Player) openFile(ctx context.Context, req OpenRequest) (Source, Info, error) {
	src, err := p.resolve(ctx, req)
	if err != nil {
		return Source{}, Info{}, err
	}
	ffprobe, err := p.tool(ctx, "ffprobe")
	if err != nil {
		return Source{}, Info{}, err
	}
	info, err := probe(ctx, ffprobe, src)
	return src, info, err
}

// evictLocked makes room for one more session by dropping the least
// recently used, and forgets expired links.
func (p *Player) evictLocked() {
	now := time.Now()
	for tok, l := range p.links {
		if now.After(l.expires) {
			delete(p.links, tok)
		}
	}
	for len(p.sessions) >= maxSessions {
		oldest, when := "", now
		for id, s := range p.sessions {
			if s.used.Before(when) {
				oldest, when = id, s.used
			}
		}
		if oldest == "" {
			return
		}
		delete(p.sessions, oldest)
	}
}

func (p *Player) session(id string) (*session, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s, ok := p.sessions[id]
	if ok {
		s.used = time.Now()
	}
	return s, ok
}

func (p *Player) sessionFor(w http.ResponseWriter, r *http.Request) (*session, bool) {
	s, ok := p.session(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Errorf("this player session has expired — reopen the video"))
	}
	return s, ok
}

// acquire takes one of the stream slots, waiting briefly: the stream a
// viewer just abandoned (a language change, a seek) takes a moment to
// notice its client is gone.
func (p *Player) acquire(ctx context.Context) bool {
	select {
	case p.slots <- struct{}{}:
		return true
	default:
	}
	t := time.NewTimer(streamSlotWait)
	defer t.Stop()
	select {
	case p.slots <- struct{}{}:
		return true
	case <-t.C:
	case <-ctx.Done():
	}
	return false
}

// pipe runs ffmpeg and copies its output to the client. Nothing is
// buffered beyond the OS pipe: a browser that stops reading stops
// ffmpeg, and a browser that goes away (the request context) kills it.
func (p *Player) pipe(w http.ResponseWriter, r *http.Request, contentType string, args []string) {
	ffmpegPath, err := p.tool(r.Context(), "ffmpeg")
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	if !p.acquire(r.Context()) {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("too many videos are playing at once — close one and try again"))
		return
	}
	defer func() { <-p.slots }()

	cmd := exec.CommandContext(r.Context(), ffmpegPath, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	var stderr strings.Builder
	cmd.Stderr = &limitedWriter{w: &stderr, n: 4096}
	if err := cmd.Start(); err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("starting ffmpeg: %w", err))
		return
	}

	// Nothing is sent until ffmpeg produces something, so a source it
	// can't open becomes an error the page can show, not an empty 200.
	buf := make([]byte, 64*1024)
	n, readErr := io.ReadAtLeast(stdout, buf, 1)
	if n == 0 {
		cmd.Wait()
		msg := strings.TrimSpace(stderr.String())
		if msg == "" && readErr != nil {
			msg = readErr.Error()
		}
		writeError(w, http.StatusBadGateway, fmt.Errorf("ffmpeg couldn't read this: %s", lastLine(msg)))
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(buf[:n])
	io.CopyBuffer(w, stdout, buf)
	cmd.Wait()
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(b []byte) (int, error) {
	if l.n > 0 {
		keep := b
		if len(keep) > l.n {
			keep = keep[:l.n]
		}
		l.w.Write(keep)
		l.n -= len(keep)
	}
	return len(b), nil
}

// input is the source as ffmpeg should open it: as it is, or — for a
// site that slows down long downloads — with every URL read through the
// relay in pieces.
func (p *Player) input(s *session) (Source, error) {
	if s.src.ChunkSize <= 0 {
		return s.src, nil
	}
	p.mu.Lock()
	routed := s.viaRelay
	p.mu.Unlock()
	if routed != nil {
		return *routed, nil
	}
	src := s.src
	src.Audio = append([]AudioInput(nil), s.src.Audio...)
	var err error
	if src.URL, err = p.relay.url(s.src.URL, "video", s.src.Header, s.src.UserAgent, s.src.ChunkSize); err != nil {
		return Source{}, err
	}
	for i := range src.Audio {
		if src.Audio[i].URL, err = p.relay.url(s.src.Audio[i].URL, "audio", s.src.Header, s.src.UserAgent, s.src.ChunkSize); err != nil {
			return Source{}, err
		}
	}
	// The relay sends the site's headers itself.
	src.Header, src.UserAgent = nil, ""
	p.mu.Lock()
	s.viaRelay = &src
	p.mu.Unlock()
	return src, nil
}

func (p *Player) handleStream(w http.ResponseWriter, r *http.Request) {
	s, ok := p.sessionFor(w, r)
	if !ok {
		return
	}
	src, err := p.input(s)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	audio, _ := strconv.Atoi(r.URL.Query().Get("audio"))
	seek, _ := strconv.ParseFloat(r.URL.Query().Get("t"), 64)
	p.pipe(w, r, "video/mp4", streamArgs(src, s.info, audio, seek))
}

// handleFile hands the source to the browser as it is, with Range
// support, for the cases the browser can play directly.
func (p *Player) handleFile(w http.ResponseWriter, r *http.Request) {
	s, ok := p.sessionFor(w, r)
	if !ok {
		return
	}
	serveOriginal(w, r, s.src, false)
}

// serveOriginal serves a file source untouched: straight off disk, or
// relayed from where it lives with the client's Range header passed on.
func serveOriginal(w http.ResponseWriter, r *http.Request, src Source, download bool) {
	if download {
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(src.Name)}))
	}
	if src.Path != "" {
		http.ServeFile(w, r, src.Path)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, src.URL, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	for name, vals := range src.Header {
		req.Header[name] = vals
	}
	for _, name := range []string{"Range", "If-Range"} {
		if v := r.Header.Get(name); v != "" {
			req.Header.Set(name, v)
		}
	}
	resp, err := relayClient.Do(req)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	defer resp.Body.Close()
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"} {
		if v := resp.Header.Get(name); v != "" {
			w.Header().Set(name, v)
		}
	}
	if w.Header().Get("Content-Type") == "" || w.Header().Get("Content-Type") == "application/octet-stream" {
		if ct := mime.TypeByExtension(path.Ext(src.Name)); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// relayClient has no overall timeout: it carries whole films. A stalled
// origin is the client's to give up on (its context cancels the request).
var relayClient = &http.Client{}

// handleSeek reports where a stream asked to start at t will really
// start. The page needs it to show the right time and line subtitles
// up, because a copied stream can only begin on a keyframe.
func (p *Player) handleSeek(w http.ResponseWriter, r *http.Request) {
	s, ok := p.sessionFor(w, r)
	if !ok {
		return
	}
	t, _ := strconv.ParseFloat(r.URL.Query().Get("t"), 64)
	start := t
	if t > 0 && s.info.Video != nil {
		if ffprobe, err := p.tool(r.Context(), "ffprobe"); err == nil {
			ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
			src, err := p.input(s)
			if err != nil {
				src = s.src
			}
			out, err := exec.CommandContext(ctx, ffprobe, keyframeArgs(src, t)...).Output()
			cancel()
			if err == nil {
				first := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
				if k, err := strconv.ParseFloat(strings.TrimRight(first, ","), 64); err == nil && k >= 0 && k <= t {
					start = k
				}
			}
		}
	}
	if t <= 0 {
		start = 0
	}
	writeJSON(w, map[string]float64{"start": start})
}

func (p *Player) handleSubs(w http.ResponseWriter, r *http.Request) {
	s, ok := p.sessionFor(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.URL.Query().Get("track"))
	if err != nil || n < 0 || n >= len(s.info.Subs) {
		writeError(w, http.StatusNotFound, fmt.Errorf("no such subtitle track"))
		return
	}
	if !s.info.Subs[n].Text {
		writeError(w, http.StatusUnsupportedMediaType, fmt.Errorf("picture-based subtitles can't be shown in the browser"))
		return
	}
	if len(s.src.Subs) > 0 {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, s.src.Subs[n].URL, nil)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		resp, err := relayClient.Do(req)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "text/vtt; charset=utf-8")
		io.Copy(w, io.LimitReader(resp.Body, 16<<20))
		return
	}
	p.pipe(w, r, "text/vtt; charset=utf-8", subsArgs(s.src, n))
}

// handleLink mints the links for playing this source outside the page.
func (p *Player) handleLink(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s, ok := p.sessionFor(w, r)
	if !ok {
		return
	}
	tok := randomID()
	p.mu.Lock()
	p.links[tok] = &extLink{session: id, expires: time.Now().Add(linkTTL)}
	p.mu.Unlock()

	name := path.Base(s.src.Name)
	if s.src.Remote {
		name += ".mkv"
	}
	base := "/s/" + tok + "/" + url.PathEscape(name)
	out := map[string]any{"stream": base, "remote": s.src.Remote}
	if !s.src.Remote {
		out["download"] = base + "?dl=1"
	}
	writeJSON(w, out)
}

// ExternalLink serves /s/{token}/{name}: the same source for an app
// that isn't the page — VLC, a phone's player, a download. The token
// in the path is the whole credential (such an app can't send the
// page's cookie), so it's long, random, per-source and short-lived.
func (p *Player) ExternalLink(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/s/"), "/", 2)
	p.mu.Lock()
	l, ok := p.links[parts[0]]
	var s *session
	if ok && time.Now().Before(l.expires) {
		s = p.sessions[l.session]
		if s != nil {
			s.used = time.Now()
		}
	}
	p.mu.Unlock()
	if s == nil {
		http.Error(w, "godl: this link has expired — make a new one from the web interface", http.StatusNotFound)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.src.Remote {
		src, err := p.input(s)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		p.pipe(w, r, "video/x-matroska", remuxAllArgs(src))
		return
	}
	serveOriginal(w, r, s.src, r.URL.Query().Get("dl") == "1")
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// WriteJSON and WriteError are the response helpers the daemon's API
// handlers share with this package, so every endpoint answers alike.
func WriteJSON(w http.ResponseWriter, v any) { writeJSON(w, v) }

func WriteError(w http.ResponseWriter, status int, err error) { writeError(w, status, err) }
