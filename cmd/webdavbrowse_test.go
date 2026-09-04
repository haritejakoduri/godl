package cmd

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// TestWebDAVBrowseDownloadWholeFolderKey is a regression test: opening a
// folder (enter) resets the cursor to its first entry, so pressing "d"
// with nothing checked only grabs that one entry — a folder's actual
// recursive download (see internal/daemon/webdav.go's startWebDAV,
// already fully recursive and structure-preserving) was only reachable
// by backing out and selecting the folder from its parent listing.
// "D" must target the folder currently being browsed (wb.path) instead,
// regardless of the cursor position or what (if anything) is checked —
// giving an unambiguous "download everything in here" action.
func TestWebDAVBrowseDownloadWholeFolderKey(t *testing.T) {
	m := statusModel{webdavBrowse: &webdavBrowseState{
		step:     webdavBrowsing,
		connName: "mynas",
		path:     "/Photos/vacation/",
		cursor:   0,
		selected: map[string]bool{"/Photos/vacation/unrelated.jpg": true},
		entries: []webdav.Entry{
			{Path: "/Photos/vacation/img1.jpg", IsDir: false},
			{Path: "/Photos/vacation/img2.jpg", IsDir: false},
		},
	}}
	mm, cmd := m.updateWebDAVBrowse(key("D"))
	m = mm.(statusModel)
	if m.webdavBrowse != nil {
		t.Fatal("D should close the browse overlay")
	}
	if cmd == nil {
		t.Fatal("D should return a command to start the download")
	}
	if m.statusMsg == "" {
		t.Error("expected a status message after starting a download")
	}
}

// TestWebDAVBrowseDownloadWholeFolderKeyNoopWhileLoading guards against
// firing a download for a stale/incomplete listing.
func TestWebDAVBrowseDownloadWholeFolderKeyNoopWhileLoading(t *testing.T) {
	m := statusModel{webdavBrowse: &webdavBrowseState{
		step:     webdavBrowsing,
		connName: "mynas",
		path:     "/Photos/",
		loading:  true,
		selected: map[string]bool{},
	}}
	mm, cmd := m.updateWebDAVBrowse(key("D"))
	m = mm.(statusModel)
	if cmd != nil {
		t.Fatal("D while loading should be a no-op")
	}
	if m.webdavBrowse == nil {
		t.Fatal("D while loading should not close the overlay")
	}
}

// TestWebDAVBrowseSearchFiltersEntries covers the "/" search flow: "/"
// opens the prompt, typed runes narrow the list live (before enter is
// even pressed), and only entries whose own name (not full path)
// contains the query, case-insensitively, remain visible.
func TestWebDAVBrowseSearchFiltersEntries(t *testing.T) {
	m := statusModel{webdavBrowse: &webdavBrowseState{
		step:     webdavBrowsing,
		connName: "mynas",
		path:     "/",
		selected: map[string]bool{},
		entries: []webdav.Entry{
			{Path: "/vacation-photos/", IsDir: true},
			{Path: "/report.pdf", IsDir: false},
			{Path: "/Report-final.docx", IsDir: false},
		},
	}}

	mm, cmd := m.updateWebDAVBrowse(key("/"))
	m = mm.(statusModel)
	if cmd != nil {
		t.Fatal("opening the search prompt shouldn't return a command")
	}
	if !m.webdavBrowse.searching {
		t.Fatal("/ should enter search mode")
	}

	for _, r := range "report" {
		mm, _ = m.updateWebDAVBrowse(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = mm.(statusModel)
	}
	if m.webdavBrowse.query != "report" {
		t.Fatalf("query = %q, want %q (should update live as you type)", m.webdavBrowse.query, "report")
	}

	visible := m.webdavBrowse.visibleEntries()
	if len(visible) != 2 {
		t.Fatalf("visibleEntries() = %v, want 2 matches (case-insensitive substring on the name)", visible)
	}
	for _, e := range visible {
		if e.Path == "/vacation-photos/" {
			t.Error("vacation-photos/ shouldn't match \"report\"")
		}
	}

	// enter confirms: still filtered, but no longer capturing keystrokes.
	mm, _ = m.updateWebDAVBrowse(key("enter"))
	m = mm.(statusModel)
	if m.webdavBrowse.searching {
		t.Fatal("enter should leave search-input mode")
	}
	if m.webdavBrowse.query != "report" {
		t.Fatal("enter should keep the filter applied, not clear it")
	}
}

// TestWebDAVBrowseSearchEscCancelsWithoutClosingOverlay is a regression
// test: esc while actively typing in the search prompt must only clear
// the in-progress search, not the whole browse overlay — a user
// backing out of a search they don't want shouldn't lose their place in
// the connection entirely.
func TestWebDAVBrowseSearchEscCancelsWithoutClosingOverlay(t *testing.T) {
	m := statusModel{webdavBrowse: &webdavBrowseState{
		step:     webdavBrowsing,
		connName: "mynas",
		path:     "/",
		selected: map[string]bool{},
		entries: []webdav.Entry{
			{Path: "/a.txt", IsDir: false},
			{Path: "/b.txt", IsDir: false},
		},
	}}
	mm, _ := m.updateWebDAVBrowse(key("/"))
	m = mm.(statusModel)
	mm, _ = m.updateWebDAVBrowse(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	m = mm.(statusModel)
	if m.webdavBrowse.query != "a" {
		t.Fatalf("query = %q, want \"a\"", m.webdavBrowse.query)
	}

	mm, cmd := m.updateWebDAVBrowse(key("esc"))
	m = mm.(statusModel)
	if m.webdavBrowse == nil {
		t.Fatal("esc while searching should not close the browse overlay")
	}
	if cmd != nil {
		t.Error("esc canceling a search shouldn't return a command")
	}
	if m.webdavBrowse.searching {
		t.Error("esc should leave search-input mode")
	}
	if m.webdavBrowse.query != "" {
		t.Errorf("query after esc = %q, want empty (cancel clears the filter)", m.webdavBrowse.query)
	}
	if len(m.webdavBrowse.visibleEntries()) != 2 {
		t.Error("canceling the search should restore the full, unfiltered entry list")
	}
}

// TestWebDAVBrowseNavigatingClearsSearch is a regression test: a filter
// scoped to one directory's contents (e.g. "report") silently carried
// into a different directory could hide entries the user has no reason
// to expect are filtered — descending into or backing out of a folder
// must reset the search.
func TestWebDAVBrowseNavigatingClearsSearch(t *testing.T) {
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
  <D:response><D:href>/dav/sub/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
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
		path:     "/",
		selected: map[string]bool{},
		cache:    map[string][]webdav.Entry{},
		entries:  []webdav.Entry{{Path: "/sub/", IsDir: true}},
	}}

	mm, _ := m.updateWebDAVBrowse(key("/"))
	m = mm.(statusModel)
	mm, _ = m.updateWebDAVBrowse(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = mm.(statusModel)
	mm, _ = m.updateWebDAVBrowse(key("enter")) // confirm search; "sub" still matches "s", so it stays visible
	m = mm.(statusModel)
	if m.webdavBrowse.query != "s" {
		t.Fatalf("query = %q, want \"s\"", m.webdavBrowse.query)
	}

	mm, cmd := m.updateWebDAVBrowse(key("enter")) // now: open /sub/, the only (filtered) entry under the cursor
	m = mm.(statusModel)
	if cmd == nil {
		t.Fatal("expected a listing command")
	}
	msg := cmd()
	mm, _ = m.Update(msg)
	m = mm.(statusModel)

	if m.webdavBrowse.query != "" {
		t.Errorf("query after navigating = %q, want empty", m.webdavBrowse.query)
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

// TestWebDAVBrowseCachesVisitedDirectories verifies that revisiting a
// directory already listed this session (going back up, then back
// down again — an extremely common browsing pattern) reuses the
// cached listing instead of issuing another PROPFIND: the returned
// tea.Cmd should be nil (nothing to run), and state should update
// synchronously.
func TestWebDAVBrowseCachesVisitedDirectories(t *testing.T) {
	var mu sync.Mutex
	propfindCount := map[string]int{}

	mux := http.NewServeMux()
	mux.HandleFunc("/dav/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		propfindCount["/dav/"]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(`<?xml version="1.0"?><D:multistatus xmlns:D="DAV:">
  <D:response><D:href>/dav/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
  <D:response><D:href>/dav/subdir/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
</D:multistatus>`))
	})
	mux.HandleFunc("/dav/subdir/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		propfindCount["/dav/subdir/"]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(`<?xml version="1.0"?><D:multistatus xmlns:D="DAV:">
  <D:response><D:href>/dav/subdir/</D:href><D:propstat><D:prop><D:resourcetype><D:collection/></D:resourcetype></D:prop><D:status>HTTP/1.1 200 OK</D:status></D:propstat></D:response>
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
		path:     "/",
		selected: map[string]bool{},
		cache:    map[string][]webdav.Entry{},
	}}

	// Root listing: real network round-trip.
	msg := listWebDAVDir(client, "/")()
	mm, _ := m.Update(msg)
	m = mm.(statusModel)
	if m.webdavBrowse.path != "/" || len(m.webdavBrowse.entries) != 1 {
		t.Fatalf("root listing didn't populate as expected: path=%q entries=%v", m.webdavBrowse.path, m.webdavBrowse.entries)
	}

	// Descend into subdir: cache miss, real round-trip.
	mm, cmd := m.updateWebDAVBrowse(key("enter"))
	m = mm.(statusModel)
	if cmd == nil {
		t.Fatal("first descent into /subdir/ should be a cache miss and return a command")
	}
	msg = cmd()
	mm, _ = m.Update(msg)
	m = mm.(statusModel)
	if m.webdavBrowse.path != "/subdir/" {
		t.Fatalf("path after descending = %q, want /subdir/", m.webdavBrowse.path)
	}

	// Back up to root: should be a cache hit (no command, no new request).
	mm, cmd = m.updateWebDAVBrowse(key("backspace"))
	m = mm.(statusModel)
	if cmd != nil {
		t.Error("backspace to an already-visited /, should be a cache hit (nil command)")
	}
	if m.webdavBrowse.path != "/" {
		t.Errorf("path after cached backspace = %q, want /", m.webdavBrowse.path)
	}

	// Down into subdir again: should also be a cache hit.
	mm, cmd = m.updateWebDAVBrowse(key("enter"))
	m = mm.(statusModel)
	if cmd != nil {
		t.Error("re-descending into an already-visited /subdir/, should be a cache hit (nil command)")
	}
	if m.webdavBrowse.path != "/subdir/" {
		t.Errorf("path after cached descent = %q, want /subdir/", m.webdavBrowse.path)
	}

	mu.Lock()
	defer mu.Unlock()
	if propfindCount["/dav/"] != 1 {
		t.Errorf("PROPFIND count for /dav/ = %d, want 1 (cache should have prevented a second request)", propfindCount["/dav/"])
	}
	if propfindCount["/dav/subdir/"] != 1 {
		t.Errorf("PROPFIND count for /dav/subdir/ = %d, want 1 (cache should have prevented a second request)", propfindCount["/dav/subdir/"])
	}
}

func TestShortenHome(t *testing.T) {
	home := "/home/alice"
	t.Setenv("HOME", home)

	cases := []struct{ in, want string }{
		{"/home/alice/Downloads", "~/Downloads"},
		{"/home/alice/Downloads/sub", "~/Downloads/sub"},
		{"/home/alice", "~"},
		{"/var/other/place", "/var/other/place"},
		{"/home/alice2/Downloads", "/home/alice2/Downloads"}, // not actually under home
	}
	for _, c := range cases {
		if got := shortenHome(c.in); got != c.want {
			t.Errorf("shortenHome(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestWebDAVNameWidth(t *testing.T) {
	cases := []struct {
		width int
		want  int
	}{
		{0, webdavNameMinWidth},  // no WindowSizeMsg yet
		{30, webdavNameMinWidth}, // narrow terminal, floor applies
		{40, 40 - 2 - 3 - 1 - 1 - webdavSizeColWidth},
		{100, 100 - 2 - 3 - 1 - 1 - webdavSizeColWidth},
		{200, 200 - 2 - 3 - 1 - 1 - webdavSizeColWidth},
	}
	for _, c := range cases {
		if got := webdavNameWidth(c.width); got != c.want {
			t.Errorf("webdavNameWidth(%d) = %d, want %d", c.width, got, c.want)
		}
	}
}

// TestWebDAVBrowseShowsDownloadDestination verifies the browse overlay
// displays where files will actually land, resolved once when
// connecting (not recomputed per keypress) and shown regardless of
// which folder the user is currently looking at.
func TestWebDAVBrowseShowsDownloadDestination(t *testing.T) {
	m := statusModel{webdavBrowse: &webdavBrowseState{
		step:      webdavBrowsing,
		connName:  "mynas",
		outputDir: "/home/alice/Downloads",
		path:      "/",
		selected:  map[string]bool{},
	}}
	view := m.viewWebDAVBrowse()
	if !strings.Contains(view, "/home/alice/Downloads") && !strings.Contains(view, "~/Downloads") {
		t.Errorf("browse view doesn't show the download destination:\n%s", view)
	}
}
