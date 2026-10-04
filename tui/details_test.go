package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"godl/internal/daemon"
	"godl/internal/store"
)

func TestDetailsOverlayShowsFiles(t *testing.T) {
	job := &daemon.JobView{Job: &store.Job{ID: "j1", Type: store.JobTorrent, Status: store.StatusActive,
		Source: "magnet:?xt=urn:btih:abc", Output: "/home/a/Downloads"}}
	m := resize(dashboardModel(job), 120, 30)

	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m = next.(statusModel)
	if m.details == nil || cmd == nil {
		t.Fatal("i should open the details overlay and ask for the file list")
	}
	if !strings.Contains(m.View(), "loading the file list") {
		t.Error("before the list arrives, the overlay should say it's loading")
	}

	next, _ = m.update(detailsLoadedMsg{st: m.details, files: []daemon.TorrentFile{
		{Index: 0, Path: "Show/S01E01.mkv", Length: 1000, Done: 1000},
		{Index: 1, Path: "Show/S01E02.mkv", Length: 1000, Done: 250},
		{Index: 2, Path: "Show/extras.nfo", Length: 10, Skipped: true},
	}})
	m = next.(statusModel)
	view := m.View()
	for _, want := range []string{"S01E01.mkv", "S01E02.mkv", "extras.nfo", "3 file(s)", "1 finished", "1 skipped", " 25%", "skip"} {
		if !strings.Contains(view, want) {
			t.Errorf("details view is missing %q:\n%s", want, view)
		}
	}

	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyEsc})
	if next.(statusModel).details != nil {
		t.Error("esc should close the overlay")
	}
}

func TestDetailsTickFilesOfARunningTorrent(t *testing.T) {
	job := &daemon.JobView{Job: &store.Job{ID: "j1", Type: store.JobTorrent, Status: store.StatusActive,
		Source: "magnet:?xt=urn:btih:abc", Output: "/home/a/Downloads"}}
	m := resize(dashboardModel(job), 120, 30)
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	m = next.(statusModel)
	files := []daemon.TorrentFile{
		{Index: 0, Path: "Show/S01E01.mkv", Length: 1000, Done: 400},
		{Index: 1, Path: "Show/S01E02.mkv", Length: 1000},
		{Index: 2, Path: "Show/extras.nfo", Length: 10, Skipped: true},
	}
	next, _ = m.update(detailsLoadedMsg{st: m.details, files: files, live: true})
	m = next.(statusModel)
	if !strings.Contains(m.View(), "[x]") || !strings.Contains(m.View(), "space tick") {
		t.Fatalf("a running torrent's file list should be tickable:\n%s", m.View())
	}

	key := func(k string) {
		t.Helper()
		var msg tea.KeyMsg
		if k == " " {
			msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune(" ")}
		} else {
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		next, _ := m.handleKey(msg)
		m = next.(statusModel)
	}
	key("j") // to S01E02
	key(" ") // untick it; the cursor moves on to extras.nfo
	key(" ") // tick extras.nfo
	if spec, n := m.details.spec(); spec != "1,3" || n != 2 {
		t.Errorf("spec = %q (%d files), want \"1,3\" (2 files)", spec, n)
	}
	if !strings.Contains(m.View(), "2 change(s) not applied") {
		t.Errorf("pending changes should be shown:\n%s", m.View())
	}

	// A refresh that already reflects one change drops it from the pending set.
	files[1].Skipped = true
	next, _ = m.update(detailsLoadedMsg{st: m.details, files: files, live: true})
	m = next.(statusModel)
	if len(m.details.pick) != 1 {
		t.Errorf("pick = %v, want only the extras.nfo change left", m.details.pick)
	}

	key("n")
	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	m = next.(statusModel)
	if cmd != nil || !strings.Contains(m.View(), "tick at least one file") {
		t.Error("applying a choice of no files should be refused before asking the daemon")
	}
	key("u")
	if len(m.details.pick) != 0 {
		t.Error("u should drop every pending change")
	}
}
