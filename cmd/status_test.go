package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/table"
	"github.com/muesli/termenv"

	"godl/internal/daemon"
	"godl/internal/store"
)

// TestBuildAddRequestDefaultsUnderDownloads is a regression test: url,
// social, and torrent jobs started from the TUI's "n" wizard (like
// their CLI counterparts — see url.go/social.go/torrent.go's RunE)
// must default their Output to the user's actual Downloads folder
// when no explicit path is given, not the current directory or godl's
// own internal data dir.
func TestBuildAddRequestDefaultsUnderDownloads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GODL_DOWNLOADS_DIR", "")
	t.Setenv("XDG_DOWNLOAD_DIR", "")
	downloads := filepath.Join(home, "Downloads")

	cases := []struct {
		apiCmd string
		source string
	}{
		{daemon.CmdAddURL, "https://example.com/files/report.pdf"},
		{daemon.CmdAddSocial, "https://example.com/watch?v=xyz"},
		{daemon.CmdAddTorrent, "magnet:?xt=urn:btih:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"},
	}
	for _, c := range cases {
		t.Run(c.apiCmd, func(t *testing.T) {
			req, err := buildAddRequest(c.apiCmd, c.source)
			if err != nil {
				t.Fatal(err)
			}
			if req.Output != downloads && !strings.HasPrefix(req.Output, downloads+string(filepath.Separator)) {
				t.Errorf("buildAddRequest(%s) Output = %q, want it under %q", c.apiCmd, req.Output, downloads)
			}
		})
	}
}

// TestColumnsForWidthGrowsWithTerminalWidth is a regression test: the
// Path/Source columns used to have a constant Width regardless of the
// terminal size, so a wide terminal still hard-truncated long download
// links, and there was no way to widen them. columnsForWidth must give
// them more room as the terminal grows, while never dropping either
// below its floor even on a very narrow terminal.
func TestColumnsForWidthGrowsWithTerminalWidth(t *testing.T) {
	narrow := columnsForWidth(40)
	wide := columnsForWidth(220)

	pathOf := func(cols []table.Column) table.Column {
		for _, c := range cols {
			if c.Title == "Path" {
				return c
			}
		}
		t.Fatal("no Path column")
		return table.Column{}
	}
	sourceOf := func(cols []table.Column) table.Column {
		for _, c := range cols {
			if c.Title == "Source" {
				return c
			}
		}
		t.Fatal("no Source column")
		return table.Column{}
	}

	if w := pathOf(narrow).Width; w < minPathWidth {
		t.Errorf("narrow Path width = %d, want >= floor %d", w, minPathWidth)
	}
	if w := sourceOf(narrow).Width; w < minSourceWidth {
		t.Errorf("narrow Source width = %d, want >= floor %d", w, minSourceWidth)
	}
	if pathOf(wide).Width <= pathOf(narrow).Width {
		t.Errorf("Path width did not grow with a wider terminal: narrow=%d wide=%d", pathOf(narrow).Width, pathOf(wide).Width)
	}
	if sourceOf(wide).Width <= sourceOf(narrow).Width {
		t.Errorf("Source width did not grow with a wider terminal: narrow=%d wide=%d", sourceOf(narrow).Width, sourceOf(wide).Width)
	}
}

// TestRebuildRowsShowsPathAndSource is a regression test: the jobs
// table used to have no column for a job's download destination at
// all, and its Source (link) column was hard-truncated at a fixed 36
// chars. rebuildRows must now emit both a Path and a Source cell, each
// with the user's home directory shortened to "~" the way the rest of
// the TUI (e.g. the WebDAV browser) already displays paths.
func TestRebuildRowsShowsPathAndSource(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory to test shortenHome against")
	}
	output := filepath.Join(home, "Downloads", "movie.mkv")
	source := "https://example.com/watch?v=abcdefghijklmnopqrstuvwxyz0123456789"

	m := statusModel{
		bar:  progress.New(progress.WithColorProfile(termenv.Ascii), progress.WithWidth(18)),
		jobs: []*daemon.JobView{{Job: &store.Job{ID: "job1", Type: store.JobURL, Status: store.StatusActive, Output: output, Source: source}, ETASeconds: -1}},
	}
	m.rebuildRows()

	rows := m.table.Rows()
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	row := rows[0]
	if len(row) != numColumns {
		t.Fatalf("row has %d cells, want %d (one per column)", len(row), numColumns)
	}
	wantPath := "~" + string(filepath.Separator) + filepath.Join("Downloads", "movie.mkv")
	if row[numColumns-2] != wantPath {
		t.Errorf("Path cell = %q, want %q", row[numColumns-2], wantPath)
	}
	if row[numColumns-1] != source {
		t.Errorf("Source cell = %q, want %q (short enough it shouldn't be pre-truncated)", row[numColumns-1], source)
	}
}

func jobView(id string) *daemon.JobView {
	return &daemon.JobView{Job: &store.Job{ID: id, Type: store.JobURL, Status: store.StatusActive}, ETASeconds: -1}
}

// TestNewestFirst is a regression test for the dashboard listing a
// newly started download at the bottom, behind everything already
// running — store.ListJobs (and so the daemon snapshot the TUI
// receives) orders oldest-created-first; newestFirst has to flip that
// for display without mutating the slice it's handed (the caller reuses
// jobsMsg's backing array as the snapshot's own record).
func TestNewestFirst(t *testing.T) {
	in := []*daemon.JobView{jobView("oldest"), jobView("middle"), jobView("newest")}
	got := newestFirst(in)

	want := []string{"newest", "middle", "oldest"}
	if len(got) != len(want) {
		t.Fatalf("newestFirst returned %d jobs, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("newestFirst[%d].ID = %q, want %q", i, got[i].ID, id)
		}
	}
	if in[0].ID != "oldest" || in[2].ID != "newest" {
		t.Error("newestFirst mutated its input slice in place")
	}
}

// TestRebuildRowsShowsSelectionCheckbox is a regression test for
// multi-select: the leading checkbox cell rebuildRows emits must
// actually reflect m.selected, not just always show unchecked.
func TestRebuildRowsShowsSelectionCheckbox(t *testing.T) {
	m := statusModel{
		bar:      progress.New(progress.WithColorProfile(termenv.Ascii), progress.WithWidth(18)),
		jobs:     []*daemon.JobView{jobView("job1"), jobView("job2")},
		selected: map[string]bool{"job2": true},
	}
	m.rebuildRows()

	rows := m.table.Rows()
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	if rows[0][0] != "[ ]" {
		t.Errorf("job1 (unselected) checkbox = %q, want \"[ ]\"", rows[0][0])
	}
	if rows[1][0] != "[x]" {
		t.Errorf("job2 (selected) checkbox = %q, want \"[x]\"", rows[1][0])
	}
}

// TestActionTargetsPrefersSelectionOverCursor is a regression test for
// bulk actions: with one or more jobs checked via space, a p/r/x/R/d/D
// keypress must act on all of them, not just whatever the cursor
// happens to be sitting on — the same "selected, or current" rule
// webdavBrowseState's own multi-select already applies to its "d" key.
func TestActionTargetsPrefersSelectionOverCursor(t *testing.T) {
	jobs := []*daemon.JobView{jobView("job1"), jobView("job2"), jobView("job3")}

	// No selection: falls back to whatever's under the cursor.
	m := statusModel{jobs: jobs, table: table.New(table.WithColumns(columnsForWidth(0)))}
	m.rebuildRows()
	m.table.SetCursor(1)
	got := m.actionTargets()
	if len(got) != 1 || got[0] != "job2" {
		t.Errorf("actionTargets() with no selection = %v, want [job2] (the cursor row)", got)
	}

	// A selection wins even if the cursor is sitting on a different,
	// unselected row.
	m.selected = map[string]bool{"job1": true, "job3": true}
	got = m.actionTargets()
	want := map[string]bool{"job1": true, "job3": true}
	if len(got) != len(want) {
		t.Fatalf("actionTargets() with a selection = %v, want %v", got, want)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("actionTargets() returned unexpected id %q", id)
		}
	}
}

// TestPruneSelectedDropsStaleIDs is a regression test: a job removed
// (by this client or another) while checked must not leave its ID
// stuck in m.selected forever — pruneSelected has to reconcile the
// selection against the current snapshot on every update.
func TestPruneSelectedDropsStaleIDs(t *testing.T) {
	m := statusModel{
		jobs:     []*daemon.JobView{jobView("job1")},
		selected: map[string]bool{"job1": true, "job-gone": true},
	}
	m.pruneSelected()

	if !m.selected["job1"] {
		t.Error("pruneSelected dropped a still-live job's selection")
	}
	if m.selected["job-gone"] {
		t.Error("pruneSelected left a stale (no-longer-listed) job selected")
	}
	if len(m.selected) != 1 {
		t.Errorf("m.selected = %v, want exactly {job1: true}", m.selected)
	}
}

// TestJobsMsgKeepsCursorOnSameJobAcrossReorder is a regression test for
// newest-first ordering's main hazard: since a newly started job lands
// at row 0, ahead of everything else, a plain index-based cursor would
// silently start pointing at whatever new job just arrived instead of
// the job the user was actually looking at. The jobsMsg handler in
// Update must re-find and refocus the same job by ID after every
// reorder.
func TestJobsMsgKeepsCursorOnSameJobAcrossReorder(t *testing.T) {
	m := statusModel{
		table: table.New(table.WithColumns(columnsForWidth(0))),
		bar:   progress.New(progress.WithColorProfile(termenv.Ascii), progress.WithWidth(18)),
	}

	next, _ := m.Update(jobsMsg([]*daemon.JobView{jobView("job1"), jobView("job2")}))
	m = next.(statusModel)
	// newestFirst puts job2 first, job1 second; move the cursor onto job1.
	m.table.SetCursor(1)
	if got := m.cursorJobID(); got != "job1" {
		t.Fatalf("cursor is on %q before the reorder, want job1", got)
	}

	// A third job starts — it lands at row 0, pushing job1/job2 down by
	// one row each relative to before.
	next, _ = m.Update(jobsMsg([]*daemon.JobView{jobView("job1"), jobView("job2"), jobView("job3")}))
	m = next.(statusModel)

	if got := m.cursorJobID(); got != "job1" {
		t.Errorf("cursor followed to %q after a new job arrived, want it to stay on job1", got)
	}
}
