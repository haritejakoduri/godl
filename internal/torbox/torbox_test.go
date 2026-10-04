package torbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fake(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New(" key-1 \n") // pasted with stray whitespace
	c.base = srv.URL
	return c
}

func TestErrorsCarryTorBoxsOwnExplanation(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer key-1" {
			t.Errorf("Authorization = %q, want the trimmed key", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{"success": false, "error": "BAD_TOKEN", "detail": "Your API token is invalid."})
	})
	_, err := c.Me(context.Background())
	if !Unauthorized(err) {
		t.Fatalf("err = %v, want it recognised as a rejected key", err)
	}
	if !strings.Contains(err.Error(), "Your API token is invalid.") {
		t.Errorf("err = %q, want TorBox's own explanation in it", err)
	}
}

func TestLinkAndTorrent(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/torrents/requestdl":
			if r.URL.Query().Get("redirect") != "" {
				t.Error("redirect links cost an API call per range request; ask for the plain link")
			}
			json.NewEncoder(w).Encode(map[string]any{"success": true, "data": "https://cdn.example/f?token=x"})
		case "/torrents/mylist":
			json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{
				"id": 7, "name": "Pack", "download_state": "completed", "download_finished": true, "download_present": false,
				"files": []map[string]any{{"id": 0, "name": "Pack/a", "size": 3}},
			}})
		}
	})
	link, err := c.Link(context.Background(), 7, 0)
	if err != nil || link != "https://cdn.example/f?token=x" {
		t.Errorf("Link = %q, %v", link, err)
	}
	tr, err := c.Torrent(context.Background(), 7)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Ready() {
		t.Error("finished but not yet present isn't ready: TorBox is still moving the files into place")
	}
	if tr.Failed() || len(tr.Files) != 1 {
		t.Errorf("torrent = %+v", tr)
	}
}

func TestNetworkErrorsDontLeakTheKey(t *testing.T) {
	c := New("secret-key")
	c.base = "http://127.0.0.1:1" // nothing listens there
	_, err := c.Link(context.Background(), 1, 2)
	if err == nil || strings.Contains(err.Error(), "secret-key") {
		t.Errorf("err = %v; want a failure that doesn't quote the API key", err)
	}
}
