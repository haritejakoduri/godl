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
