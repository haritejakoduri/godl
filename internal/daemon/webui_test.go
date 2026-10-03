package daemon

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"godl/internal/store"
)

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func get(t *testing.T, url string) int {
	t.Helper()
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

// The web interface exists only while switched on: settings start it,
// stop it, and move it, and a refused change leaves things as they were.
func TestWebUIFollowsSettings(t *testing.T) {
	d := newTestDaemon(t)
	t.Cleanup(d.closeWebUI)
	ctx := context.Background()
	port := freePort(t)
	base := "http://127.0.0.1:" + strconv.Itoa(port) + "/"

	if get(t, base) != 0 {
		t.Fatal("something is listening before the web interface was turned on")
	}

	s := store.DefaultSettings()
	s.WebUI, s.WebUIPort = true, port
	if _, err := d.applySettings(ctx, s); err != nil {
		t.Fatalf("turning it on: %v", err)
	}
	if code := get(t, base); code != http.StatusUnauthorized {
		t.Fatalf("on, local mode, no token: got %d, want 401", code)
	}
	tok, err := os.ReadFile(filepath.Join(d.dataDir, "webui.token"))
	if err != nil || len(strings.TrimSpace(string(tok))) < 32 {
		t.Fatalf("no usable token file: %v", err)
	}

	// Network mode without credentials is refused, and nothing changes.
	bad := s
	bad.WebUINetwork = true
	if _, err := d.applySettings(ctx, bad); err == nil {
		t.Fatal("network mode without a username and password was accepted")
	}
	if d.cachedSettings().WebUINetwork {
		t.Fatal("a refused change was saved anyway")
	}
	if code := get(t, base); code != http.StatusUnauthorized {
		t.Fatalf("after a refused change: got %d, want the server still up (401)", code)
	}

	// A taken port is a refused change too, not "on" with nothing listening.
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	moved := s
	moved.WebUIPort = busy.Addr().(*net.TCPAddr).Port
	if _, err := d.applySettings(ctx, moved); err == nil {
		t.Fatal("moving to a taken port was accepted")
	}
	if d.cachedSettings().WebUIPort != port {
		t.Fatal("a refused port change was saved anyway")
	}

	s.WebUI = false
	if _, err := d.applySettings(ctx, s); err != nil {
		t.Fatalf("turning it off: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	if code := get(t, base); code != 0 {
		t.Fatalf("off: something still answers (%d)", code)
	}
}

func TestWebEventPayloadLeavesOutSecrets(t *testing.T) {
	d := newTestDaemon(t)
	ctx := context.Background()
	j := &store.Job{ID: "j1", Type: store.JobURL, Source: "https://x/y", Output: "/tmp/y", Status: store.StatusPaused,
		Options: store.JobOptions{Headers: []string{"Authorization: Bearer topsecret"}, CookiesFile: "/home/a/cookies.txt"}}
	if err := d.st.CreateJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	payload := string(d.webEventPayload())
	if !strings.Contains(payload, `"id":"j1"`) || !strings.Contains(payload, `"status":"paused"`) {
		t.Fatalf("payload is missing the job: %s", payload)
	}
	for _, secret := range []string{"topsecret", "cookies.txt"} {
		if strings.Contains(payload, secret) {
			t.Errorf("payload leaks %q: %s", secret, payload)
		}
	}
	if strings.Contains(payload, "\n") {
		t.Error("an event payload must be one line")
	}
}

func TestBuildAddRequests(t *testing.T) {
	dl := t.TempDir()
	t.Setenv("GODL_DOWNLOADS_DIR", dl)

	reqs, err := buildAddRequests(webAddRequest{Type: "social", Links: []string{" https://a/watch ", "", "# comment", "https://b"}, Preset: "720p", Rate: "2M"})
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 || reqs[0].Source != "https://a/watch" || reqs[0].Cmd != CmdAddSocial {
		t.Fatalf("links not cleaned up: %+v", reqs)
	}
	if reqs[0].Format == "" || reqs[0].LimitRate != 2*1024*1024 || reqs[0].Output != dl {
		t.Errorf("preset/rate/output not applied: %+v", reqs[0])
	}

	reqs, err = buildAddRequests(webAddRequest{Type: "torrent", Links: []string{"magnet:?xt=urn:btih:abc"}, TorrentFiles: "1,3-4", SeedRatio: 1.5, SeedTime: "2h"})
	if err != nil {
		t.Fatal(err)
	}
	if o := reqs[0].Options; o.TorrentFiles != "1,3-4" || o.SeedRatio != 1.5 || o.SeedTimeSec != 7200 {
		t.Errorf("torrent options not applied: %+v", o)
	}

	for name, in := range map[string]webAddRequest{
		"no links":       {Type: "url"},
		"bad rate":       {Type: "url", Links: []string{"https://a/b"}, Rate: "fast"},
		"bad checksum":   {Type: "url", Links: []string{"https://a/b"}, Sha256: "abc"},
		"bad header":     {Type: "url", Links: []string{"https://a/b"}, Headers: []string{"no colon"}},
		"bad selection":  {Type: "torrent", Links: []string{"magnet:?x"}, TorrentFiles: "5-2"},
		"bad seed time":  {Type: "torrent", Links: []string{"magnet:?x"}, SeedTime: "soon"},
		"unknown type":   {Type: "ftp", Links: []string{"x"}},
		"unknown preset": {Type: "social", Links: []string{"https://a"}, Preset: "8k"},
	} {
		if _, err := buildAddRequests(in); err == nil {
			t.Errorf("%s: accepted, want an error", name)
		}
	}
}

func TestWebSettingsNeverCarryThePassword(t *testing.T) {
	ws := webSettingsOf(store.Settings{WebUIPassword: "pw", WebUIUsername: "alice"})
	if ws.WebUIPassword != "" || !ws.WebUIPasswordSet {
		t.Errorf("webSettingsOf = %+v, want no password but password_set", ws)
	}
	if ws.WebUIPort != store.DefaultWebUIPort {
		t.Errorf("unset port should show as the default, got %d", ws.WebUIPort)
	}
}
