package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"godl/internal/connections"
	"godl/internal/fileserver"
	"godl/internal/jobreq"
	"godl/internal/paths"
	"godl/internal/ratelimit"
	"godl/internal/reqhdr"
	"godl/internal/social"
	"godl/internal/store"
	"godl/internal/torrentmgr"
	"godl/internal/urlname"
	"godl/internal/version"
	"godl/internal/webdav"
	"godl/internal/webui"
)

// webRoutes registers the page's API. Every handler here runs behind
// webui.Guard, and every state change goes through d.do — the same
// function the socket uses — so the page can't do anything "godl" on
// the command line couldn't, or mean something different by it.
func (d *Daemon) webRoutes(mux *http.ServeMux, ws *webServer) {
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) { d.webState(w, r, ws) })
	mux.HandleFunc("GET /api/events", func(w http.ResponseWriter, r *http.Request) { webui.ServeEvents(w, r, ws.events) })
	mux.HandleFunc("POST /api/jobs", d.webAddJobs)
	mux.HandleFunc("POST /api/jobs/action", d.webJobsAction)
	mux.HandleFunc("GET /api/jobs/{id}/files", d.webJobFiles)
	mux.HandleFunc("GET /api/jobs/{id}/details", d.webJobDetails)
	mux.HandleFunc("POST /api/torrent/files", d.webTorrentFiles)
	mux.HandleFunc("POST /api/torrent/upload", d.webTorrentUpload)
	mux.HandleFunc("PUT /api/settings", d.webPutSettings)
	mux.HandleFunc("POST /api/connections", d.webAddConnection)
	mux.HandleFunc("DELETE /api/connections/{name}", d.webRemoveConnection)
	mux.HandleFunc("GET /api/webdav/list", d.webWebDAVList)
	mux.HandleFunc("POST /api/webdav/download", d.webWebDAVDownload)
	mux.HandleFunc("POST /api/share", d.webShareStart)
	mux.HandleFunc("DELETE /api/share", func(w http.ResponseWriter, r *http.Request) {
		d.stopShare()
		webui.WriteJSON(w, map[string]bool{"ok": true})
	})
}

// maxBody bounds a JSON request body. The largest legitimate one is a
// pasted list of links.
const maxBody = 1 << 20

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(v); err != nil {
		webui.WriteError(w, http.StatusBadRequest, fmt.Errorf("bad request: %w", err))
		return false
	}
	return true
}

func badRequest(w http.ResponseWriter, err error) {
	webui.WriteError(w, http.StatusBadRequest, err)
}

// webSettings is store.Settings as the page reads and writes it. The
// password only travels inward: the page learns whether one is set,
// and sends a new one to change it.
type webSettings struct {
	MaxConcurrent        int    `json:"max_concurrent"`
	DefaultRateLimit     string `json:"default_rate_limit"`
	GlobalRateLimit      string `json:"global_rate_limit"`
	AutoRetry            bool   `json:"auto_retry"`
	AutoRetryMaxAttempts int    `json:"auto_retry_max_attempts"`
	NotifyOnComplete     bool   `json:"notify_on_complete"`
	WebUI                bool   `json:"webui"`
	WebUIPort            int    `json:"webui_port"`
	WebUINetwork         bool   `json:"webui_network"`
	WebUIUsername        string `json:"webui_username"`
	WebUIPassword        string `json:"webui_password,omitempty"`
	WebUIPasswordSet     bool   `json:"webui_password_set"`
}

func webSettingsOf(s store.Settings) webSettings {
	port := s.WebUIPort
	if port == 0 {
		port = store.DefaultWebUIPort
	}
	return webSettings{
		MaxConcurrent: s.MaxConcurrent, DefaultRateLimit: s.DefaultRateLimit, GlobalRateLimit: s.GlobalRateLimit,
		AutoRetry: s.AutoRetry, AutoRetryMaxAttempts: s.AutoRetryMaxAttempts, NotifyOnComplete: s.NotifyOnComplete,
		WebUI: s.WebUI, WebUIPort: port, WebUINetwork: s.WebUINetwork, WebUIUsername: s.WebUIUsername,
		WebUIPasswordSet: s.WebUIPassword != "",
	}
}

type webConnection struct {
	Name     string `json:"name"`
	URL      string `json:"url"`
	Username string `json:"username"`
	Insecure bool   `json:"insecure"`
}

func webConnections() []webConnection {
	conns, _ := connections.List()
	out := make([]webConnection, len(conns))
	for i, c := range conns {
		out[i] = webConnection{Name: c.Name, URL: c.URL, Username: c.Username, Insecure: c.Insecure}
	}
	return out
}

// webState is everything the page needs once, on load; what changes
// from then on arrives over /api/events.
func (d *Daemon) webState(w http.ResponseWriter, r *http.Request, ws *webServer) {
	downloads, _ := paths.DownloadsDir()
	type preset struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	presets := make([]preset, len(social.Presets))
	for i, p := range social.Presets {
		presets[i] = preset{p.Name, p.Description}
	}
	webui.WriteJSON(w, map[string]any{
		"version":     version.Version,
		"downloads":   downloads,
		"presets":     presets,
		"settings":    webSettingsOf(d.cachedSettings()),
		"connections": webConnections(),
		"share":       d.webShareState(),
		"network":     ws.key.network,
		"lan":         fileserver.ReachableIPs(),
	})
}

func (d *Daemon) webPutSettings(w http.ResponseWriter, r *http.Request) {
	var in webSettings
	if !readJSON(w, r, &in) {
		return
	}
	cur := d.cachedSettings()
	s := store.Settings{
		MaxConcurrent: in.MaxConcurrent, DefaultRateLimit: strings.TrimSpace(in.DefaultRateLimit),
		GlobalRateLimit: strings.TrimSpace(in.GlobalRateLimit),
		AutoRetry:       in.AutoRetry, AutoRetryMaxAttempts: in.AutoRetryMaxAttempts, NotifyOnComplete: in.NotifyOnComplete,
		WebUI: in.WebUI, WebUIPort: in.WebUIPort, WebUINetwork: in.WebUINetwork,
		WebUIUsername: strings.TrimSpace(in.WebUIUsername), WebUIPassword: cur.WebUIPassword,
	}
	if in.WebUIPassword != "" {
		s.WebUIPassword = in.WebUIPassword
	}
	resp := d.do(r.Context(), Request{Cmd: CmdSetSettings, Settings: &s})
	if !resp.OK {
		badRequest(w, fmt.Errorf("%s", resp.Error))
		return
	}
	webui.WriteJSON(w, webSettingsOf(*resp.Settings))
}

// webAddRequest is the "new download" form.
type webAddRequest struct {
	Type   string   `json:"type"` // "url" | "social" | "torrent"
	Links  []string `json:"links"`
	Output string   `json:"output"`
	Rate   string   `json:"rate"`

	// url
	Concurrency int    `json:"concurrency"`
	Sha256      string `json:"sha256"`
	// url, social
	Headers     []string `json:"headers"`
	Cookie      string   `json:"cookie"`
	CookiesFile string   `json:"cookies_file"`
	// social
	Preset             string `json:"preset"`
	Format             string `json:"format"`
	CookiesFromBrowser string `json:"cookies_from_browser"`
	// torrent
	TorrentFiles string  `json:"torrent_files"`
	SeedRatio    float64 `json:"seed_ratio"`
	SeedTime     string  `json:"seed_time"`
}

var sha256Re = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// buildAddRequests turns the form into one daemon Request per link,
// resolving outputs exactly as the CLI's flags do (see internal/jobreq).
func buildAddRequests(in webAddRequest) ([]Request, error) {
	var links []string
	for _, l := range in.Links {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			links = append(links, l)
		}
	}
	if len(links) == 0 {
		return nil, fmt.Errorf("paste at least one link")
	}
	var limit int64
	if rate := strings.TrimSpace(in.Rate); rate != "" {
		parsed, err := ratelimit.ParseRate(rate)
		if err != nil {
			return nil, err
		}
		limit = parsed
	}

	var opts store.JobOptions
	if in.Type == "url" || in.Type == "social" {
		for _, h := range in.Headers {
			if h = strings.TrimSpace(h); h != "" {
				opts.Headers = append(opts.Headers, h)
			}
		}
		if c := strings.TrimSpace(in.Cookie); c != "" {
			opts.Headers = append(opts.Headers, "Cookie: "+c)
		}
		for _, h := range opts.Headers {
			if _, _, err := reqhdr.ParseHeader(h); err != nil {
				return nil, err
			}
		}
		if f := strings.TrimSpace(in.CookiesFile); f != "" {
			abs, err := paths.ResolveOutput(f)
			if err != nil {
				return nil, err
			}
			if _, err := reqhdr.Build(nil, abs); err != nil {
				return nil, err
			}
			opts.CookiesFile = abs
		}
	}

	reqs := make([]Request, len(links))
	switch in.Type {
	case "url":
		sum := strings.TrimSpace(in.Sha256)
		if sum != "" && !sha256Re.MatchString(sum) {
			return nil, fmt.Errorf("the checksum must be 64 hexadecimal characters")
		}
		if sum != "" && len(links) > 1 {
			return nil, fmt.Errorf("a checksum belongs to one file; start that link by itself")
		}
		hdr, err := reqhdr.Build(opts.Headers, opts.CookiesFile)
		if err != nil {
			return nil, err
		}
		outputs, err := jobreq.URLOutputs(links, strings.TrimSpace(in.Output), func(link string) string { return urlname.FromURL(link, hdr) })
		if err != nil {
			return nil, err
		}
		concurrency := in.Concurrency
		if concurrency < 1 {
			concurrency = 4
		}
		for i, link := range links {
			reqs[i] = Request{Cmd: CmdAddURL, Source: link, Output: outputs[i], Concurrency: concurrency,
				LimitRate: limit, Sha256: strings.ToLower(sum), Options: opts}
		}

	case "social":
		format := strings.TrimSpace(in.Format)
		if in.Preset != "" && format == "" {
			p, ok := social.Lookup(in.Preset)
			if !ok {
				return nil, fmt.Errorf("unknown quality %q", in.Preset)
			}
			format = p.Format
		}
		opts.CookiesFromBrowser = strings.TrimSpace(in.CookiesFromBrowser)
		output, err := jobreq.OutputPath(strings.TrimSpace(in.Output), "")
		if err != nil {
			return nil, err
		}
		if err := os.MkdirAll(output, 0o755); err != nil {
			return nil, err
		}
		for i, link := range links {
			reqs[i] = Request{Cmd: CmdAddSocial, Source: link, Output: output, Format: format, LimitRate: limit, Options: opts}
		}

	case "torrent":
		if _, err := torrentmgr.ParseSelection(in.TorrentFiles); err != nil {
			return nil, err
		}
		if in.SeedRatio < 0 {
			return nil, fmt.Errorf("the seed ratio can't be negative")
		}
		var seedSec int64
		if st := strings.TrimSpace(in.SeedTime); st != "" {
			dur, err := time.ParseDuration(st)
			if err != nil || dur < 0 {
				return nil, fmt.Errorf("seed time %q isn't a duration — write it like 30m or 2h", st)
			}
			seedSec = int64(dur.Seconds())
		}
		output, err := jobreq.OutputPath(strings.TrimSpace(in.Output), "")
		if err != nil {
			return nil, err
		}
		for i, link := range links {
			if !strings.HasPrefix(link, "magnet:") {
				abs, err := paths.ResolveOutput(link)
				if err != nil {
					return nil, err
				}
				link = abs
			}
			reqs[i] = Request{Cmd: CmdAddTorrent, Source: link, Output: output, LimitRate: limit}
			reqs[i].Options.TorrentFiles = in.TorrentFiles
			reqs[i].Options.SeedRatio = in.SeedRatio
			reqs[i].Options.SeedTimeSec = seedSec
		}

	default:
		return nil, fmt.Errorf("unknown download type %q", in.Type)
	}
	return reqs, nil
}

type webFailure struct {
	Source string `json:"source"`
	Error  string `json:"error"`
}

func (d *Daemon) webAddJobs(w http.ResponseWriter, r *http.Request) {
	var in webAddRequest
	if !readJSON(w, r, &in) {
		return
	}
	reqs, err := buildAddRequests(in)
	if err != nil {
		badRequest(w, err)
		return
	}
	// One bad link doesn't stop the rest, as with "godl url -i".
	started := []string{}
	failed := []webFailure{}
	for _, req := range reqs {
		resp := d.do(r.Context(), req)
		switch {
		case !resp.OK:
			failed = append(failed, webFailure{req.Source, resp.Error})
		case resp.Job != nil && resp.Job.Status == store.StatusFailed:
			failed = append(failed, webFailure{req.Source, resp.Job.ErrorMsg})
		default:
			started = append(started, resp.Job.ID)
		}
	}
	webui.WriteJSON(w, map[string]any{"started": started, "failed": failed})
}

func (d *Daemon) webJobsAction(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IDs    []string `json:"ids"`
		Action string   `json:"action"`
		Purge  bool     `json:"purge"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	cmds := map[string]string{"pause": CmdPause, "resume": CmdResume, "retry": CmdRetry, "cancel": CmdCancel, "remove": CmdRemove}
	cmd, ok := cmds[in.Action]
	if !ok {
		badRequest(w, fmt.Errorf("unknown action %q", in.Action))
		return
	}
	done := 0
	failed := []webFailure{}
	for _, id := range in.IDs {
		resp := d.do(r.Context(), Request{Cmd: cmd, JobID: id, Purge: in.Purge && cmd == CmdRemove})
		if resp.OK {
			done++
		} else {
			failed = append(failed, webFailure{id, resp.Error})
		}
	}
	webui.WriteJSON(w, map[string]any{"ok": done, "failed": failed})
}

func (d *Daemon) webTorrentFiles(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Source string `json:"source"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	source := strings.TrimSpace(in.Source)
	if source == "" {
		badRequest(w, fmt.Errorf("paste a magnet link or a .torrent path first"))
		return
	}
	if !strings.HasPrefix(source, "magnet:") {
		abs, err := paths.ResolveOutput(source)
		if err != nil {
			badRequest(w, err)
			return
		}
		source = abs
	}
	resp := d.do(r.Context(), Request{Cmd: CmdTorrentFiles, Source: source})
	if !resp.OK {
		webui.WriteError(w, http.StatusBadGateway, fmt.Errorf("%s", resp.Error))
		return
	}
	webui.WriteJSON(w, map[string]any{"name": resp.Name, "files": resp.Files, "source": source})
}

// maxTorrentUpload bounds an uploaded .torrent. Real ones run from a
// few KB to a couple of MB for the very largest.
const maxTorrentUpload = 16 << 20

var unsafeNameRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// webTorrentUpload stores a .torrent the browser sent and returns the
// path to pass as a torrent job's source — the way a phone, which has
// no path on this machine to type, adds one.
func (d *Daemon) webTorrentUpload(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, maxTorrentUpload+1))
	if err != nil {
		badRequest(w, err)
		return
	}
	if len(data) == 0 || len(data) > maxTorrentUpload {
		badRequest(w, fmt.Errorf("that isn't a .torrent file (it's empty or larger than 16 MB)"))
		return
	}
	// Every .torrent is a bencoded dictionary.
	if data[0] != 'd' {
		badRequest(w, fmt.Errorf("that isn't a .torrent file"))
		return
	}
	dir := filepath.Join(d.dataDir, "uploads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		webui.WriteError(w, http.StatusInternalServerError, err)
		return
	}
	name := unsafeNameRe.ReplaceAllString(filepath.Base(r.URL.Query().Get("name")), "_")
	name = strings.TrimSuffix(strings.Trim(name, "._"), ".torrent")
	if name == "" {
		name = "upload"
	}
	var rnd [4]byte
	rand.Read(rnd[:])
	dest := filepath.Join(dir, hex.EncodeToString(rnd[:])+"-"+name+".torrent")
	if err := os.WriteFile(dest, data, 0o600); err != nil {
		webui.WriteError(w, http.StatusInternalServerError, err)
		return
	}
	webui.WriteJSON(w, map[string]string{"path": dest})
}

var connectionNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func (d *Daemon) webAddConnection(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		URL      string `json:"url"`
		Username string `json:"username"`
		Password string `json:"password"`
		Insecure bool   `json:"insecure"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	in.Name, in.URL = strings.TrimSpace(in.Name), strings.TrimSpace(in.URL)
	if !connectionNameRe.MatchString(in.Name) {
		badRequest(w, fmt.Errorf("the name may contain only letters, digits, - and _"))
		return
	}
	if !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
		badRequest(w, fmt.Errorf("the address must start with http:// or https://"))
		return
	}
	err := connections.Add(connections.Connection{
		Name: in.Name, Type: connections.TypeWebDAV, URL: in.URL,
		Username: strings.TrimSpace(in.Username), Password: in.Password, Insecure: in.Insecure,
	})
	if err != nil {
		webui.WriteError(w, http.StatusInternalServerError, err)
		return
	}
	webui.WriteJSON(w, webConnections())
}

func (d *Daemon) webRemoveConnection(w http.ResponseWriter, r *http.Request) {
	if err := connections.Remove(r.PathValue("name")); err != nil {
		webui.WriteError(w, http.StatusNotFound, err)
		return
	}
	webui.WriteJSON(w, webConnections())
}

func webdavClient(name string) (*webdav.Client, error) {
	conn, err := connections.Get(name)
	if err != nil {
		return nil, err
	}
	return webdav.New(conn.URL, conn.Username, conn.Password, conn.Insecure)
}

// webdavListTimeout matches the patience the downloads themselves have
// with a rate-limiting server (see internal/webdav's retries).
const webdavListTimeout = 3 * time.Minute

func (d *Daemon) webWebDAVList(w http.ResponseWriter, r *http.Request) {
	client, err := webdavClient(r.URL.Query().Get("conn"))
	if err != nil {
		webui.WriteError(w, http.StatusNotFound, err)
		return
	}
	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir = "/"
	}
	ctx, cancel := context.WithTimeout(r.Context(), webdavListTimeout)
	defer cancel()
	entries, err := client.List(ctx, dir)
	if err != nil {
		webui.WriteError(w, http.StatusBadGateway, err)
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Path) < strings.ToLower(entries[j].Path)
	})
	type entry struct {
		Path string `json:"path"`
		Name string `json:"name"`
		Dir  bool   `json:"dir"`
		Size int64  `json:"size"`
	}
	out := make([]entry, len(entries))
	for i, e := range entries {
		out[i] = entry{Path: e.Path, Name: path.Base(strings.TrimSuffix(e.Path, "/")), Dir: e.IsDir, Size: e.Size}
	}
	webui.WriteJSON(w, map[string]any{"path": dir, "entries": out})
}

func (d *Daemon) webWebDAVDownload(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Conn   string   `json:"conn"`
		Paths  []string `json:"paths"`
		Output string   `json:"output"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if _, err := connections.Get(in.Conn); err != nil {
		webui.WriteError(w, http.StatusNotFound, err)
		return
	}
	output, err := jobreq.OutputPath(strings.TrimSpace(in.Output), "")
	if err != nil {
		badRequest(w, err)
		return
	}
	started := []string{}
	failed := []webFailure{}
	for _, p := range in.Paths {
		resp := d.do(r.Context(), Request{Cmd: CmdAddWebDAV, Source: JoinWebDAVSource(in.Conn, p), Output: output})
		if resp.OK {
			started = append(started, resp.Job.ID)
		} else {
			failed = append(failed, webFailure{p, resp.Error})
		}
	}
	webui.WriteJSON(w, map[string]any{"started": started, "failed": failed, "output": output})
}

func (d *Daemon) webShareStart(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Dir            string `json:"dir"`
		Host           string `json:"host"`
		Port           int    `json:"port"`
		Username       string `json:"username"`
		Password       string `json:"password"`
		AllowWrite     bool   `json:"allow_write"`
		SelfSigned     bool   `json:"self_signed"`
		InsecureNoAuth bool   `json:"insecure_no_auth"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	dir := strings.TrimSpace(in.Dir)
	if dir == "" {
		dl, err := paths.DownloadsDir()
		if err != nil {
			badRequest(w, err)
			return
		}
		dir = dl
	}
	dir, err := paths.ResolveOutput(dir)
	if err != nil {
		badRequest(w, err)
		return
	}
	host := strings.TrimSpace(in.Host)
	if host == "" {
		host = "0.0.0.0"
	}
	if in.Port == 0 {
		in.Port = 8080
	}
	err = d.startShare(fileserver.RunConfig{
		Config: fileserver.Config{Root: dir, Username: strings.TrimSpace(in.Username), Password: in.Password, ReadOnly: !in.AllowWrite},
		Host:   host, Port: in.Port, SelfSigned: in.SelfSigned, InsecureNoAuth: in.InsecureNoAuth,
	})
	if err != nil {
		badRequest(w, err)
		return
	}
	webui.WriteJSON(w, d.webShareState())
}

// playableFile is one file of a job the player can be pointed at.
type playableFile struct {
	File int    `json:"file"` // what to send back as OpenRequest.File
	Name string `json:"name"`
	Size int64  `json:"size"`
	path string
}

// jobFiles lists what's playable in a job: the files of a torrent
// that's still running (served through the stream server, so the
// pieces the player wants are fetched first), or whatever a finished
// job left on disk.
func (d *Daemon) jobFiles(id string) (job *store.Job, files []playableFile, running bool, err error) {
	job, err = d.st.GetJob(context.Background(), id)
	if err != nil {
		return nil, nil, false, fmt.Errorf("job %s not found", id)
	}
	onDisk := job.Status == store.StatusCompleted || job.Status == store.StatusSeeding
	if !onDisk {
		if job.Type != store.JobTorrent {
			return job, nil, false, fmt.Errorf("this download hasn't finished yet — it can be played once it has")
		}
		_, tfiles, err := d.streamTorrent(id, -1)
		if err != nil {
			return job, nil, true, err
		}
		_, selected := d.tm.Files(id)
		for i, f := range tfiles {
			if i < len(selected) && !selected[i] {
				continue
			}
			files = append(files, playableFile{File: f.Index + 1, Name: f.Path, Size: f.Length})
		}
		return job, files, true, nil
	}

	var roots []string
	switch job.Type {
	case store.JobURL:
		roots = []string{job.Output}
	case store.JobTorrent:
		for _, p := range job.ResolvedPaths {
			roots = append(roots, filepath.Join(job.Output, p))
		}
	default:
		roots = job.ResolvedPaths
	}
	for _, root := range roots {
		filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			fi, err := e.Info()
			if err != nil {
				return nil
			}
			name := filepath.Base(p)
			if rel, err := filepath.Rel(filepath.Dir(root), p); err == nil {
				name = filepath.ToSlash(rel)
			}
			files = append(files, playableFile{Name: name, Size: fi.Size(), path: p})
			return nil
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	for i := range files {
		files[i].File = i + 1
	}
	if len(files) == 0 {
		return job, nil, false, fmt.Errorf("the downloaded file is no longer where godl saved it")
	}
	return job, files, false, nil
}

// mediaFiles narrows files to audio/video, unless that leaves nothing.
func mediaFiles(files []playableFile) []playableFile {
	var media []playableFile
	for _, f := range files {
		if streamableExts[strings.ToLower(path.Ext(f.Name))] {
			media = append(media, f)
		}
	}
	if len(media) == 0 {
		return files
	}
	return media
}

// webJobDetails is a job's file list with per-file progress, for the
// page's expandable row. The page asks only while a row is open.
func (d *Daemon) webJobDetails(w http.ResponseWriter, r *http.Request) {
	resp := d.do(r.Context(), Request{Cmd: CmdJobFiles, JobID: r.PathValue("id")})
	if !resp.OK {
		webui.WriteError(w, http.StatusNotFound, fmt.Errorf("%s", resp.Error))
		return
	}
	webui.WriteJSON(w, map[string]any{"files": resp.Files, "note": resp.Note})
}

func (d *Daemon) webJobFiles(w http.ResponseWriter, r *http.Request) {
	_, files, _, err := d.jobFiles(r.PathValue("id"))
	if err != nil {
		webui.WriteError(w, http.StatusConflict, err)
		return
	}
	webui.WriteJSON(w, map[string]any{"files": mediaFiles(files)})
}

// resolveMedia is the player's Resolver: it turns "job X" or "this
// file on that connection" into something ffmpeg can open. The page
// never names a path, so there's no path here to validate — only job
// IDs and connection names godl itself already knows.
func (d *Daemon) resolveMedia(ctx context.Context, req webui.OpenRequest) (webui.Source, error) {
	switch req.Kind {
	case "job":
		_, files, running, err := d.jobFiles(req.Job)
		if err != nil {
			return webui.Source{}, err
		}
		pick := playableFile{}
		if req.File > 0 {
			for _, f := range files {
				if f.File == req.File {
					pick = f
				}
			}
			if pick.File == 0 {
				return webui.Source{}, fmt.Errorf("that job has no file #%d", req.File)
			}
		} else {
			for _, f := range mediaFiles(files) {
				if f.Size >= pick.Size {
					pick = f
				}
			}
		}
		if !running {
			return webui.Source{Name: path.Base(pick.Name), Path: pick.path}, nil
		}
		u, _, err := d.streamTorrent(req.Job, pick.File-1)
		if err != nil {
			return webui.Source{}, err
		}
		return webui.Source{Name: path.Base(pick.Name), URL: u}, nil

	case "webdav":
		client, err := webdavClient(req.Conn)
		if err != nil {
			return webui.Source{}, err
		}
		src := webui.Source{Name: path.Base(req.Path), URL: client.URLFor(req.Path).String()}
		if auth := client.AuthHeader(); auth != "" {
			src.Header = http.Header{"Authorization": {auth}}
		}
		return src, nil
	}
	return webui.Source{}, fmt.Errorf("unknown thing to play: %q", req.Kind)
}
