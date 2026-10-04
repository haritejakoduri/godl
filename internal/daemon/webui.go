package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"godl/internal/fileserver"
	"godl/internal/guide"
	"godl/internal/store"
	"godl/internal/webui"
)

// webServer is the browser interface while it's switched on. It exists
// only between "Web interface: on" and "off" in the settings — with it
// off (the default) the daemon has no HTTP listener for it, no
// goroutine and no timer, so a godl that never uses the web interface
// pays nothing for its existing.
type webServer struct {
	srv    *http.Server
	key    webKey
	addr   string
	events *webui.Broadcaster
}

// webKey is everything a running server was started with; a settings
// change that leaves it alone leaves the server alone.
type webKey struct {
	port     int
	network  bool
	username string
	password string
}

func webKeyOf(s store.Settings) webKey {
	port := s.WebUIPort
	if port == 0 {
		port = store.DefaultWebUIPort
	}
	k := webKey{port: port, network: s.WebUINetwork}
	if s.WebUINetwork {
		k.username, k.password = s.WebUIUsername, s.WebUIPassword
	}
	return k
}

// validateWebSettings rejects the combinations that would either not
// start or start unsafely. Network mode without credentials is the one
// that matters: the page can download to any folder and delete files.
func validateWebSettings(s store.Settings) error {
	if s.WebUIPort < 0 || s.WebUIPort > 65535 {
		return fmt.Errorf("web interface port must be between 1 and 65535")
	}
	if s.WebUINetwork && (s.WebUIUsername == "" || s.WebUIPassword == "") {
		return fmt.Errorf("opening the web interface to other devices needs a username and a password — anyone who could reach it could otherwise start and delete downloads")
	}
	return nil
}

// eventInterval matches the socket subscription's cadence (see
// streamSnapshots): the page is no more demanding than "godl status".
const eventInterval = 500 * time.Millisecond

// syncWebUI makes the running server match s: started, stopped,
// restarted on a changed address or credentials, or left alone.
func (d *Daemon) syncWebUI(s store.Settings) error {
	d.webMu.Lock()
	defer d.webMu.Unlock()

	want := webKeyOf(s)
	if d.web != nil {
		if s.WebUI && d.web.key == want {
			return nil
		}
		d.stopWebLocked()
	}
	if !s.WebUI {
		return nil
	}

	host := "127.0.0.1"
	auth := webui.Auth{}
	if want.network {
		host = "0.0.0.0"
		auth.Username, auth.Password = want.username, want.password
	} else {
		tok, err := webui.Token(d.dataDir)
		if err != nil {
			return fmt.Errorf("starting the web interface: %w", err)
		}
		auth.Token = tok
	}
	// The exact port or nothing: unlike "godl serve", this address gets
	// bookmarked, so quietly moving to the next free port would be
	// worse than saying the port is taken.
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(want.port)))
	if err != nil {
		return fmt.Errorf("starting the web interface: %w", err)
	}

	ws := &webServer{key: want, addr: ln.Addr().String()}
	ws.events = webui.NewBroadcaster(eventInterval, d.webEventPayload)
	player := webui.NewPlayer(d.resolveMedia)

	mux := http.NewServeMux()
	d.webRoutes(mux, ws)
	player.Routes(mux)
	mux.HandleFunc("/s/", player.ExternalLink)
	mux.HandleFunc("GET /guide", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The guide is one file with its own inline style and script,
		// which the interface's policy would block.
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; frame-ancestors 'none'")
		w.Write(guide.HTML)
	})
	mux.Handle("/", webui.Static())

	ws.srv = &http.Server{
		Handler:           webui.Guard(auth, mux, "/s/"),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := ws.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("web interface: %v", err)
		}
	}()
	d.web = ws
	log.Printf("web interface listening on %s", ws.addr)
	return nil
}

// stopWebLocked takes the server down without cutting off the request
// that asked for it — turning the interface off from its own settings
// page still gets its answer. Shutdown releases the port at once (so a
// restart on the same port works immediately) and then waits for
// requests to finish; the event feeds never do, so they're closed after
// a moment.
func (d *Daemon) stopWebLocked() {
	ws := d.web
	d.web = nil
	if ws == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := ws.srv.Shutdown(ctx); err != nil {
			ws.srv.Close()
		}
		close(done)
	}()
	// Shutdown closes the listener before it does anything else; give
	// it that long so the caller can rebind the port.
	select {
	case <-done:
	case <-time.After(50 * time.Millisecond):
	}
}

func (d *Daemon) closeWebUI() {
	d.webMu.Lock()
	if d.web != nil {
		d.web.srv.Close()
		d.web = nil
	}
	share := d.share
	d.share = nil
	d.webMu.Unlock()
	if share != nil {
		share.Close()
	}
}

// webJob is a job as the page sees it: what a row shows, and nothing
// else. Notably not the job's headers and cookies — they can hold
// login tokens, and the page has no use for them.
type webJob struct {
	ID      string  `json:"id"`
	Type    string  `json:"type"`
	Status  string  `json:"status"`
	Source  string  `json:"source"`
	Output  string  `json:"output"`
	Done    int64   `json:"done"`
	Total   int64   `json:"total"`
	Speed   float64 `json:"speed"`
	ETA     int64   `json:"eta"`
	Upload  float64 `json:"upload,omitempty"`
	Ratio   float64 `json:"ratio,omitempty"`
	Error   string  `json:"error,omitempty"`
	Created int64   `json:"created"`
	Via     string  `json:"via,omitempty"`   // "torbox" for a torrent fetched through TorBox
	Phase   string  `json:"phase,omitempty"` // what TorBox is doing with it
}

type webShare struct {
	Root       string   `json:"root"`
	URLs       []string `json:"urls"`
	AllowWrite bool     `json:"allow_write"`
	Username   string   `json:"username"`
	TLS        bool     `json:"tls"`
}

func (d *Daemon) webShareState() *webShare {
	d.webMu.Lock()
	r := d.share
	d.webMu.Unlock()
	if r == nil {
		return nil
	}
	return &webShare{Root: r.Root, URLs: r.URLs(), AllowWrite: r.AllowWrite, Username: r.Username, TLS: r.TLS}
}

// webEventPayload is one frame of the page's live feed.
func (d *Daemon) webEventPayload() []byte {
	views := d.snapshot()
	jobs := make([]webJob, len(views))
	for i, v := range views {
		jobs[i] = webJob{
			ID: v.ID, Type: string(v.Type), Status: string(v.Status),
			Source: v.Source, Output: v.Output,
			Done: v.BytesDone, Total: v.BytesTotal,
			Speed: v.SpeedBps, ETA: v.ETASeconds, Upload: v.UploadBps, Ratio: v.Ratio,
			Error: v.ErrorMsg, Created: v.CreatedAt.Unix(),
			Via: v.Options.Via, Phase: v.Phase,
		}
	}
	payload, err := json.Marshal(struct {
		Jobs  []webJob  `json:"jobs"`
		Share *webShare `json:"share"`
	}{jobs, d.webShareState()})
	if err != nil {
		return []byte(`{"jobs":[]}`)
	}
	return payload
}

// startShare and stopShare run the web interface's "share a folder":
// the same server as "godl serve", living in the daemon so it carries
// on after the tab that started it is closed.
func (d *Daemon) startShare(cfg fileserver.RunConfig) error {
	d.webMu.Lock()
	defer d.webMu.Unlock()
	if d.share != nil {
		return fmt.Errorf("already sharing %s — stop that first", d.share.Root)
	}
	r, err := fileserver.Start(cfg)
	if err != nil {
		return err
	}
	d.share = r
	return nil
}

func (d *Daemon) stopShare() {
	d.webMu.Lock()
	r := d.share
	d.share = nil
	d.webMu.Unlock()
	if r != nil {
		r.Close()
	}
}
