package cmd

import (
	"net/http"
	"net/http/httptest"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"godl/internal/connections"
	"godl/internal/webdav"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "up", "down", "left", "enter", "backspace":
		return tea.KeyMsg{Type: map[string]tea.KeyType{
			"up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft,
			"enter": tea.KeyEnter, "backspace": tea.KeyBackspace,
		}[s]}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func TestWebDAVBrowsePickConnectionNavigation(t *testing.T) {
	m := statusModel{webdavBrowse: &webdavBrowseState{
		step: webdavPickConn,
		conns: []connections.Connection{
			{Name: "a"}, {Name: "b"}, {Name: "c"},
		},
	}}

	mm, _ := m.updateWebDAVBrowse(key("down"))
	m = mm.(statusModel)
	if m.webdavBrowse.connIndex != 1 {
		t.Fatalf("connIndex after down = %d, want 1", m.webdavBrowse.connIndex)
	}

	// Doesn't run off the end of the list.
	mm, _ = m.updateWebDAVBrowse(key("down"))
	m = mm.(statusModel)
	mm, _ = m.updateWebDAVBrowse(key("down"))
	m = mm.(statusModel)
	if m.webdavBrowse.connIndex != 2 {
		t.Fatalf("connIndex after overshooting down = %d, want 2 (clamped)", m.webdavBrowse.connIndex)
	}

	mm, _ = m.updateWebDAVBrowse(key("up"))
	m = mm.(statusModel)
	if m.webdavBrowse.connIndex != 1 {
		t.Fatalf("connIndex after up = %d, want 1", m.webdavBrowse.connIndex)
	}

	mm, _ = m.updateWebDAVBrowse(key("esc"))
	m = mm.(statusModel)
	if m.webdavBrowse != nil {
		t.Fatal("esc at the connection-picker step should close the overlay")
	}
}

func TestWebDAVBrowseSelectEntryTogglesSelection(t *testing.T) {
	m := statusModel{webdavBrowse: &webdavBrowseState{
		step:     webdavBrowsing,
		connName: "mynas",
		path:     "/",
		selected: map[string]bool{},
		entries: []webdav.Entry{
			{Path: "/a.txt", IsDir: false, Size: 10},
			{Path: "/sub/", IsDir: true, Size: -1},
		},
	}}

	mm, _ := m.updateWebDAVBrowse(key("down"))
	m = mm.(statusModel)
	if m.webdavBrowse.cursor != 1 {
		t.Fatalf("cursor after down = %d, want 1", m.webdavBrowse.cursor)
	}

	mm, _ = m.updateWebDAVBrowse(key(" "))
	m = mm.(statusModel)
	if !m.webdavBrowse.selected["/sub/"] {
		t.Fatal("space should have selected /sub/")
	}

	// Toggling again deselects.
	mm, _ = m.updateWebDAVBrowse(key(" "))
	m = mm.(statusModel)
	if m.webdavBrowse.selected["/sub/"] {
		t.Fatal("space again should have deselected /sub/")
	}
}

func TestWebDAVBrowseDownloadUsesSelectionOrCursor(t *testing.T) {
	// With nothing selected, "d" targets the entry under the cursor.
	m := statusModel{webdavBrowse: &webdavBrowseState{
		step:     webdavBrowsing,
		connName: "mynas",
		path:     "/",
		selected: map[string]bool{},
		entries: []webdav.Entry{
			{Path: "/a.txt", IsDir: false},
			{Path: "/sub/", IsDir: true},
		},
	}}
	mm, cmd := m.updateWebDAVBrowse(key("d"))
	m = mm.(statusModel)
	if m.webdavBrowse != nil {
		t.Fatal("d should close the browse overlay")
	}
	if cmd == nil {
		t.Fatal("d should return a command to start the download")
	}
	if m.statusMsg == "" {
		t.Error("expected a status message after starting a download")
	}

	// With entries selected, "d" targets the selection instead of the
	// cursor, regardless of what the cursor is on.
	m = statusModel{webdavBrowse: &webdavBrowseState{
		step:     webdavBrowsing,
		connName: "mynas",
		path:     "/",
		cursor:   0, // on /a.txt, but /sub/ is what's selected
		selected: map[string]bool{"/sub/": true},
		entries: []webdav.Entry{
			{Path: "/a.txt", IsDir: false},
			{Path: "/sub/", IsDir: true},
		},
	}}
	mm, cmd = m.updateWebDAVBrowse(key("d"))
	m = mm.(statusModel)
	if m.webdavBrowse != nil || cmd == nil {
		t.Fatal("d with a selection should close the overlay and return a command")
	}
}

func TestWebDAVBrowseEnterDescendsOnlyIntoDirectories(t *testing.T) {
	m := statusModel{webdavBrowse: &webdavBrowseState{
		step:     webdavBrowsing,
		connName: "mynas",
		path:     "/",
		selected: map[string]bool{},
		entries: []webdav.Entry{
			{Path: "/a.txt", IsDir: false},
		},
	}}
	// Cursor is on a file: enter should be a no-op (no listing kicked off).
	mm, cmd := m.updateWebDAVBrowse(key("enter"))
	m = mm.(statusModel)
	if cmd != nil || m.webdavBrowse.loading {
		t.Fatal("enter on a file should not trigger a directory listing")
	}
}

func TestWebDAVBrowseBackspaceAtRootIsNoop(t *testing.T) {
	m := statusModel{webdavBrowse: &webdavBrowseState{
		step:     webdavBrowsing,
		connName: "mynas",
		path:     "/",
		selected: map[string]bool{},
	}}
	mm, cmd := m.updateWebDAVBrowse(key("backspace"))
	m = mm.(statusModel)
	if cmd != nil || m.webdavBrowse.loading {
		t.Fatal("backspace at the root should be a no-op, not attempt to list a parent")
	}
}

// TestWebDAVBrowseBackspaceGoesToParentNotSameDir is a regression test:
// path.Dir on a trailing-slash directory path (the form WebDAV hrefs
// always use for collections, e.g. "/sub/") used to return "/sub"
// unchanged instead of "/", so "go up a directory" silently re-listed
// the current directory instead of ascending.
func TestWebDAVBrowseBackspaceGoesToParentNotSameDir(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/dav/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(`<?xml version="1.0"?><D:multistatus xmlns:D="DAV:">
  <D:response><D:href>/dav/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
</D:multistatus>`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client, err := webdav.New(srv.URL+"/dav/", "", "", false)
	if err != nil {
		t.Fatal(err)
	}

	m := statusModel{webdavBrowse: &webdavBrowseState{
		step:     webdavBrowsing,
		connName: "mynas",
		client:   client,
		path:     "/sub/",
		selected: map[string]bool{},
	}}
	_, cmd := m.updateWebDAVBrowse(key("backspace"))
	if cmd == nil {
		t.Fatal("backspace from /sub/ should return a listing command")
	}
	msg := cmd()
	listed, ok := msg.(webdavListedMsg)
	if !ok {
		t.Fatalf("expected webdavListedMsg, got %#v", msg)
	}
	if listed.path != "/" {
		t.Errorf("backspace from /sub/ listed %q, want parent \"/\"", listed.path)
	}
}
