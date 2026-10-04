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
