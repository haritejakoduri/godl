package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"godl/internal/daemon"
	"godl/internal/webdav"
)

func resize(m statusModel, w, h int) statusModel {
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return next.(statusModel)
}

func manyJobs(n int) []*daemon.JobView {
	jobs := make([]*daemon.JobView, n)
	for i := range jobs {
		jobs[i] = jobView(fmt.Sprintf("job%d", i))
	}
	return jobs
}

// The screen is a fixed grid: a View taller than the terminal has its top
// cut off (title first), and a line wider than it is cut at the edge.
func assertFits(t *testing.T, view string, w, h int) {
	t.Helper()
	assertHeight(t, view, w, h)
	assertWidth(t, view, w, h)
}

func assertHeight(t *testing.T, view string, w, h int) {
	t.Helper()
	if got := lipgloss.Height(view); got > h {
		t.Errorf("view is %d lines on a %dx%d terminal — the top would scroll off:\n%s", got, w, h, view)
	}
}

func assertWidth(t *testing.T, view string, w, h int) {
	t.Helper()
	for _, line := range strings.Split(view, "\n") {
		if lw := lipgloss.Width(line); lw > w {
			t.Errorf("line is %d cells on a %dx%d terminal: %q", lw, w, h, line)
		}
	}
}

// TestDashboardFitsAndKeepsHelpOnNarrowTerminals: the help line is ~160
// characters. On a narrow terminal it used to be cut off ("q quit" was
// the part lost), and once wrapped it must not push the title off the top.
func TestDashboardFitsAndKeepsHelpOnNarrowTerminals(t *testing.T) {
	for _, size := range [][2]int{{200, 40}, {100, 30}, {60, 24}, {40, 20}} {
		w, h := size[0], size[1]
		t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
			m := resize(dashboardModel(manyJobs(60)...), w, h)
			view := m.View()
			// The table itself has a 134-cell minimum width (see
			// columnsForWidth) and is cut at the edge below that; the
			// footer is what must wrap.
			assertHeight(t, view, w, h)
			assertWidth(t, m.dashboardFooter(), w, h)
			if !strings.Contains(view, "godl status") {
				t.Error("title scrolled off the top")
			}
			// Wrapping may split "q quit" across lines, so compare without whitespace.
			if flat := strings.Join(strings.Fields(view), ""); !strings.Contains(flat, "qquit") {
				t.Errorf("help text lost its tail:\n%s", view)
			}

			// A long message must still fit, not just the empty footer.
			m.statusMsg = "error: " + strings.Repeat("something went wrong ", 8)
			m.err = fmt.Errorf("dial unix /run/user/1000/godl.sock: connect: connection refused")
			m = resize(m, w, h)
			assertHeight(t, m.View(), w, h)
			assertWidth(t, m.dashboardFooter(), w, h)
		})
	}
}

// TestBrowseFitsTheTerminal: the list height used to be a fixed
// "height - 5" although the surrounding chrome takes more lines than
// that, so a long folder overflowed and lost its title.
func TestBrowseFitsTheTerminal(t *testing.T) {
	entries := make([]webdav.Entry, 300)
	for i := range entries {
		entries[i] = webdav.Entry{Path: fmt.Sprintf("/dir/file%03d.mkv", i), Size: 1 << 20}
	}
	for _, size := range [][2]int{{120, 40}, {60, 24}, {40, 16}} {
		w, h := size[0], size[1]
		t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
			m := statusModel{width: w, height: h, webdavBrowse: &webdavBrowseState{
				step: webdavBrowsing, connName: "nas", path: "/dir/",
				outputDir: "/home/alice/Downloads", entries: entries,
				selected: map[string]bool{}, cursor: 150,
			}}
			view := m.viewWebDAVBrowse()
			assertFits(t, view, w, h)
			if !strings.Contains(view, "nas:/dir/") {
				t.Error("browser title scrolled off the top")
			}
			if !strings.Contains(view, "file150") {
				t.Error("the cursor row is not on screen")
			}
			m.webdavBrowse.searching = true
			m.webdavBrowse.searchInput = textinput.New()
			assertFits(t, m.viewWebDAVBrowse(), w, h)
		})
	}
}

// TestFitCellsAlignsWideCharacters: names in CJK or with emoji take two
// cells a character, so both the pad and the cut have to count cells.
func TestFitCellsAlignsWideCharacters(t *testing.T) {
	for _, s := range []string{"a.mkv", "映画の名前.mkv", strings.Repeat("長い", 30), "🎬movie🎬", strings.Repeat("x", 100), ""} {
		got := fitCells(s, 20)
		if w := lipgloss.Width(got); w != 20 {
			t.Errorf("fitCells(%q, 20) is %d cells wide: %q", s, w, got)
		}
	}
}

// TestCursorStaysOnScreenWhenTheTableResizes: the table is resized
// whenever the footer changes height (a wrapped error appears, the
// terminal shrinks), and the row being watched must not be left below the
// new window.
func TestCursorStaysOnScreenWhenTheTableResizes(t *testing.T) {
	for _, size := range [][2]int{{160, 30}, {100, 24}} {
		w, h := size[0], size[1]
		t.Run(fmt.Sprintf("%dx%d", w, h), func(t *testing.T) {
			m := resize(dashboardModel(manyJobs(80)...), w, h)
			for i := 0; i < 50; i++ {
				next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
				m = next.(statusModel)
			}
			want := m.cursorJobID()
			check := func(stage string) {
				t.Helper()
				if !strings.Contains(m.View(), want) {
					t.Errorf("%s: cursor row %s is not on screen (table height %d)", stage, want, m.tableHeight)
				}
			}
			check("after scrolling")

			// The footer grows: a long connection error wraps onto several lines.
			m.err = fmt.Errorf("dial: %s", strings.Repeat("x", 200))
			next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
			m = next.(statusModel)
			want = m.cursorJobID()
			check("after the footer grew")

			m = resize(m, w, h-6)
			check("after the terminal shrank")
			m = resize(m, w, h+10)
			check("after the terminal grew")

			// A new job lands at the top, moving the cursor's row index.
			next, _ = m.Update(jobsMsg(append(manyJobs(80), jobView("jobnew"))))
			m = next.(statusModel)
			if got := m.cursorJobID(); got != want {
				t.Errorf("cursor moved from %s to %s when a job arrived", want, got)
			}
			check("after a new job arrived")
		})
	}
}
