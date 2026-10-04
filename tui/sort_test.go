package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"godl/internal/daemon"
	"godl/internal/store"
	"godl/internal/webdav"
)

func jv(id, out string, st store.JobStatus, done, total int64, speed float64, eta int64) *daemon.JobView {
	return &daemon.JobView{Job: &store.Job{ID: id, Type: store.JobURL, Output: out, Status: st, BytesDone: done, BytesTotal: total},
		SpeedBps: speed, ETASeconds: eta}
}

func ids(jobs []*daemon.JobView) string {
	s := ""
	for _, j := range jobs {
		s += j.ID
	}
	return s
}

func TestSortJobs(t *testing.T) {
	// Store order: oldest first.
	jobs := []*daemon.JobView{
		jv("a", "/d/zeta.iso", store.StatusCompleted, 100, 100, 0, -1),
		jv("b", "/d/Alpha.iso", store.StatusActive, 10, 400, 5, 60),
		jv("c", "/d/mid.iso", store.StatusPaused, 150, 200, 0, -1),
		jv("d", "/d/beta.iso", store.StatusActive, 30, 300, 9, 20),
	}
	cases := []struct {
		by      jobSort
		reverse bool
		want    string
	}{
		{sortNewest, false, "dcba"},
		{sortNewest, true, "abcd"},
		{sortName, false, "bdca"},   // case-insensitive
		{sortStatus, false, "dbca"}, // active (newest first), paused, completed
		{sortProgress, false, "acdb"},
		{sortSize, false, "bdca"},
		{sortSpeed, false, "dbca"},
		{sortTimeLeft, false, "dbca"}, // known estimates first, soonest first
		{sortSize, true, "acdb"},
	}
	for _, c := range cases {
		if got := ids(sortJobs(jobs, c.by, c.reverse)); got != c.want {
			t.Errorf("sortJobs(%s, reverse=%v) = %s, want %s", c.by, c.reverse, got, c.want)
		}
	}
	if ids(jobs) != "abcd" {
		t.Error("sortJobs changed its input")
	}
}

func TestSortEntriesKeepsFoldersFirst(t *testing.T) {
	now := time.Now()
	entries := []webdav.Entry{
		{Path: "/b.mkv", Size: 10, ModTime: now.Add(-time.Hour)},
		{Path: "/Zdir/", IsDir: true, Size: -1},
		{Path: "/a.mkv", Size: 30, ModTime: now},
		{Path: "/adir/", IsDir: true, Size: -1},
	}
	paths := func(es []webdav.Entry) string {
		s := ""
		for _, e := range es {
			s += e.Path + " "
		}
		return s
	}
	for _, c := range []struct {
		by      entrySort
		reverse bool
		want    string
	}{
		{entryByName, false, "/adir/ /Zdir/ /a.mkv /b.mkv "},
		{entryByName, true, "/Zdir/ /adir/ /b.mkv /a.mkv "},
		{entryBySize, false, "/adir/ /Zdir/ /a.mkv /b.mkv "},
		{entryByModified, false, "/adir/ /Zdir/ /a.mkv /b.mkv "},
		{entryByModified, true, "/Zdir/ /adir/ /b.mkv /a.mkv "},
	} {
		if got := paths(sortEntries(entries, c.by, c.reverse)); got != c.want {
			t.Errorf("sortEntries(%s, reverse=%v) = %q, want %q", c.by, c.reverse, got, c.want)
		}
	}
}

func TestDashboardSortKeys(t *testing.T) {
	jobs := []*daemon.JobView{
		jv("a", "/d/zeta.iso", store.StatusCompleted, 100, 100, 0, -1),
		jv("b", "/d/alpha.iso", store.StatusActive, 10, 400, 5, 60),
	}
	m := resize(dashboardModel(), 120, 30)
	m = m.applyJobs(jobsMsg(jobs))
	if ids(m.jobs) != "ba" {
		t.Fatalf("default order = %s, want newest first", ids(m.jobs))
	}
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")}) // -> name
	m = next.(statusModel)
	if m.sortBy != sortName || ids(m.jobs) != "ba" {
		t.Fatalf("after t: sortBy=%s order=%s, want name order ba", m.sortBy, ids(m.jobs))
	}
	next, _ = m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("T")})
	m = next.(statusModel)
	if ids(m.jobs) != "ab" {
		t.Fatalf("after T: order=%s, want ab", ids(m.jobs))
	}
}
