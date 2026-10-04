package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"godl/internal/store"
	"godl/internal/torbox"
)

// fakeTorBox is just enough of TorBox's API for a torrent job: it adds
// a torrent, reports it downloading once and then ready, hands out
// links to its files (served from the same server, ranges and all),
// and records deletes.
type fakeTorBox struct {
	srv   *httptest.Server
	files map[int64][]byte
	names map[int64]string
	state string // what mylist reports once "ready" would be due

	mu      sync.Mutex
	polls   int
	deleted []int64
	added   []string
}

func newFakeTorBox(t *testing.T) *fakeTorBox {
	t.Helper()
	f := &fakeTorBox{
		files: map[int64][]byte{0: bytes.Repeat([]byte("a"), 300_000), 1: bytes.Repeat([]byte("b"), 200_000), 2: []byte("info")},
		names: map[int64]string{0: "Pack/a.bin", 1: "Pack/sub/b.bin", 2: "Pack/c.nfo"},
		state: "completed",
	}
	reply := func(w http.ResponseWriter, ok bool, data any, detail string) {
		w.Header().Set("Content-Type", "application/json")
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
		}
		json.NewEncoder(w).Encode(map[string]any{"success": ok, "data": data, "detail": detail, "error": nil})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/api/torrents/createtorrent", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key-1" {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "AUTH_ERROR", "detail": "bad key"})
			return
		}
		r.ParseMultipartForm(1 << 20)
		f.mu.Lock()
		f.added = append(f.added, r.FormValue("magnet"))
		f.mu.Unlock()
		reply(w, true, map[string]any{"torrent_id": 42, "hash": "abc"}, "added")
	})
	mux.HandleFunc("GET /v1/api/torrents/mylist", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.polls++
		first := f.polls == 1
		state := f.state
		f.mu.Unlock()
		t := map[string]any{"id": 42, "hash": "abc", "name": "Pack", "size": 500_004,
			"download_state": "downloading", "progress": 0.5, "download_speed": 1 << 20, "seeds": 7}
		if !first {
			t["download_state"] = state
			t["progress"] = 1.0
			t["download_finished"] = state == "completed"
			t["download_present"] = state == "completed"
			var files []map[string]any
			for id := int64(0); id < 3; id++ {
				files = append(files, map[string]any{"id": id, "name": f.names[id], "short_name": filepath.Base(f.names[id]), "size": len(f.files[id])})
			}
			t["files"] = files
		}
		reply(w, true, t, "")
	})
	mux.HandleFunc("GET /v1/api/torrents/requestdl", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("token") != "key-1" {
			reply(w, false, nil, "bad token")
			return
		}
		reply(w, true, f.srv.URL+"/cdn/"+r.URL.Query().Get("file_id"), "")
	})
	mux.HandleFunc("GET /cdn/{id}", func(w http.ResponseWriter, r *http.Request) {
		var id int64
		fmt.Sscan(r.PathValue("id"), &id)
		http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(f.files[id]))
	})
	mux.HandleFunc("POST /v1/api/torrents/controltorrent", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			TorrentID int64  `json:"torrent_id"`
			Operation string `json:"operation"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if in.Operation == "delete" {
			f.mu.Lock()
			f.deleted = append(f.deleted, in.TorrentID)
			f.mu.Unlock()
		}
		reply(w, true, nil, "")
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)

	oldBase, oldPoll := torbox.BaseURL, torboxPoll
	torbox.BaseURL, torboxPoll = f.srv.URL+"/v1/api", 20*time.Millisecond
	t.Cleanup(func() { torbox.BaseURL, torboxPoll = oldBase, oldPoll })
	return f
}

const testMagnet = "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"

func torboxDaemon(t *testing.T) *Daemon {
	d := newTestDaemon(t)
	s := store.DefaultSettings()
	s.TorBoxAPIKey = "key-1"
	d.settings = s
	return d
}

func TestTorBoxJobDownloadsTheChosenFiles(t *testing.T) {
	fake := newFakeTorBox(t)
	d := torboxDaemon(t)
	out := t.TempDir()
	j := &store.Job{ID: "t1", Type: store.JobTorrent, Source: testMagnet, Output: out, Status: store.StatusQueued,
		Options: store.JobOptions{Via: store.ViaTorBox, TorrentFiles: "1-2"}}
	if err := d.st.CreateJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	d.start(j)
	final := waitForTerminal(t, d, "t1")
	if final.Status != store.StatusCompleted {
		t.Fatalf("status %s (%s), want completed", final.Status, final.ErrorMsg)
	}

	for rel, want := range map[string][]byte{"Pack/a.bin": fake.files[0], "Pack/sub/b.bin": fake.files[1]} {
		got, err := os.ReadFile(filepath.Join(out, rel))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s: %d bytes, err %v; want %d bytes", rel, len(got), err, len(want))
		}
	}
	if _, err := os.Stat(filepath.Join(out, "Pack/c.nfo")); !os.IsNotExist(err) {
		t.Error("c.nfo wasn't chosen and shouldn't be downloaded")
	}
	if final.BytesDone != 500_000 {
		t.Errorf("BytesDone = %d, want 500000 (the chosen files)", final.BytesDone)
	}
	if len(final.ResolvedPaths) != 1 || final.ResolvedPaths[0] != "Pack" {
		t.Errorf("ResolvedPaths = %v, want [Pack] so remove --purge deletes just the torrent's folder", final.ResolvedPaths)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.added) != 1 || fake.added[0] != testMagnet {
		t.Errorf("added %v, want the magnet once", fake.added)
	}
	if len(fake.deleted) != 1 || fake.deleted[0] != 42 {
		t.Errorf("deleted %v, want the torrent removed from TorBox afterwards", fake.deleted)
	}
	if final.Options.TorBoxID != 0 {
		t.Errorf("TorBoxID = %d, want it cleared once deleted there", final.Options.TorBoxID)
	}
}

func TestTorBoxJobResumesWithoutFetchingFinishedFilesAgain(t *testing.T) {
	fake := newFakeTorBox(t)
	d := torboxDaemon(t)
	d.settings.TorBoxKeep = true
	out := t.TempDir()
	// a.bin finished in an earlier run; b.bin didn't start.
	os.MkdirAll(filepath.Join(out, "Pack"), 0o755)
	os.WriteFile(filepath.Join(out, "Pack/a.bin"), fake.files[0], 0o644)
	j := &store.Job{ID: "t2", Type: store.JobTorrent, Source: testMagnet, Output: out, Status: store.StatusQueued,
		Options: store.JobOptions{Via: store.ViaTorBox, TorBoxID: 42, TorrentFiles: "1-2"}}
	d.st.CreateJob(context.Background(), j)
	fake.files[0] = bytes.Repeat([]byte("X"), 300_000) // what a re-download would write
	d.start(j)
	final := waitForTerminal(t, d, "t2")
	if final.Status != store.StatusCompleted {
		t.Fatalf("status %s (%s)", final.Status, final.ErrorMsg)
	}
	if got, _ := os.ReadFile(filepath.Join(out, "Pack/a.bin")); got[0] != 'a' {
		t.Error("a.bin was downloaded again; a finished file should be kept")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.added) != 0 {
		t.Error("a job with a TorBox ID should pick that torrent up, not add it again")
	}
	if len(fake.deleted) != 0 {
		t.Error("with \"keep in TorBox\" on, nothing should be deleted there")
	}
}

func TestTorBoxJobFailsWhenTorBoxGivesUp(t *testing.T) {
	fake := newFakeTorBox(t)
	fake.state = "error"
	d := torboxDaemon(t)
	j := &store.Job{ID: "t3", Type: store.JobTorrent, Source: testMagnet, Output: t.TempDir(), Status: store.StatusQueued,
		Options: store.JobOptions{Via: store.ViaTorBox}}
	d.st.CreateJob(context.Background(), j)
	d.start(j)
	final := waitForTerminal(t, d, "t3")
	if final.Status != store.StatusFailed || !strings.Contains(final.ErrorMsg, "TorBox couldn't download") {
		t.Errorf("status %s, error %q; want failed, saying TorBox couldn't download it", final.Status, final.ErrorMsg)
	}
}

func TestResolveVia(t *testing.T) {
	d := newTestDaemon(t)
	if _, err := d.resolveVia(store.JobOptions{Via: store.ViaTorBox}); err == nil {
		t.Error("TorBox without an API key should be refused")
	}
	if o, _ := d.resolveVia(store.JobOptions{}); o.Via != "" {
		t.Errorf("no key, no choice: Via = %q, want godl's own client", o.Via)
	}

	d.settings.TorBoxAPIKey = "k"
	if o, _ := d.resolveVia(store.JobOptions{}); o.Via != "" {
		t.Error("with the default off, an open choice should stay on godl's own client")
	}
	d.settings.TorBoxDefault = true
	if o, _ := d.resolveVia(store.JobOptions{}); o.Via != store.ViaTorBox {
		t.Error("with the default on, an open choice should go through TorBox")
	}
	if o, _ := d.resolveVia(store.JobOptions{Via: store.ViaP2P}); o.Via != "" {
		t.Error("an explicit p2p should win over the default")
	}
	if _, err := d.resolveVia(store.JobOptions{Via: "carrier-pigeon"}); err == nil {
		t.Error("an unknown way should be refused")
	}
}

func TestTorBoxLocalPathStaysInsideOutput(t *testing.T) {
	out := t.TempDir()
	got := torboxLocalPath(out, torbox.File{Name: "../../etc/passwd", ShortName: "passwd"})
	if !strings.HasPrefix(got, out+string(filepath.Separator)) {
		t.Errorf("torboxLocalPath = %q, escapes %q", got, out)
	}
	if got := torboxDisplayPath("Pack", torbox.File{Name: "Pack/sub/b.bin"}); got != "sub/b.bin" {
		t.Errorf("display path = %q, want sub/b.bin (as godl's own client lists it)", got)
	}
}
