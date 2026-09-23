package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"godl/internal/daemon"
	"godl/internal/store"
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

func browsingModel() statusModel {
	return statusModel{webdavBrowse: &webdavBrowseState{
		step: webdavBrowsing, path: "/", selected: map[string]bool{},
		cache: map[string][]webdav.Entry{},
	}}
}

// TestStaleWebDAVListingsAreIgnored: a slow PROPFIND that answers after
// the user closed the browser, reopened it, or moved to another folder
// must not overwrite what they're now looking at.
func TestStaleWebDAVListingsAreIgnored(t *testing.T) {
	old := &webdavBrowseState{}
	stale := []webdav.Entry{{Path: "/stale/"}}
	fresh := []webdav.Entry{{Path: "/fresh.txt"}}

	t.Run("from a previous browser session", func(t *testing.T) {
		m := browsingModel()
		m.webdavBrowse.loading, m.webdavBrowse.pending = true, "/"
		next, _ := m.Update(webdavListedMsg{wb: old, path: "/", entries: stale})
		wb := next.(statusModel).webdavBrowse
		if len(wb.entries) != 0 || !wb.loading {
			t.Errorf("a reply from another session was applied: %+v", wb.entries)
		}
		next, _ = m.Update(webdavListErrMsg{wb: old, path: "/", err: fmt.Errorf("boom")})
		if wb := next.(statusModel).webdavBrowse; wb.err != "" || !wb.loading {
			t.Errorf("an error from another session was applied: %q", wb.err)
		}
	})

	t.Run("for a folder the user already left", func(t *testing.T) {
		m := browsingModel()
		wb := m.webdavBrowse
		wb.loading, wb.pending = true, "/b/"
		next, _ := m.Update(webdavListedMsg{wb: wb, path: "/a/", entries: stale})
		if got := next.(statusModel).webdavBrowse; len(got.entries) != 0 || got.path != "/" {
			t.Errorf("a listing for /a/ replaced the view while /b/ is loading: path=%q", got.path)
		}
		next, _ = m.Update(webdavListedMsg{wb: wb, path: "/b/", entries: fresh})
		if got := next.(statusModel).webdavBrowse; len(got.entries) != 1 || got.path != "/b/" || got.loading {
			t.Errorf("the awaited listing was not applied: %+v", got)
		}
	})
}

// TestStaleSettingsRepliesAreIgnored is the same guard for the Settings
// tab: a reply for a tab that was closed and reopened isn't the new tab's.
func TestStaleSettingsRepliesAreIgnored(t *testing.T) {
	old := &settingsState{}
	m := statusModel{settings: &settingsState{loading: true}}
	next, _ := m.Update(settingsLoadedMsg{st: old, settings: store.Settings{MaxConcurrent: 99}})
	got := next.(statusModel).settings
	if !got.loading || got.current.MaxConcurrent == 99 {
		t.Errorf("a reply for a previous Settings tab was applied: %+v", got)
	}

	next, _ = m.Update(settingsLoadedMsg{st: m.settings, settings: store.Settings{MaxConcurrent: 7}})
	if got := next.(statusModel).settings; got.loading || got.current.MaxConcurrent != 7 {
		t.Errorf("the awaited reply was not applied: %+v", got)
	}
}

// TestSettingsSavedIndicatorClearsOnNavigation: "saved" is documented as
// lasting until the next interaction, but moving the cursor left it up.
func TestSettingsSavedIndicatorClearsOnNavigation(t *testing.T) {
	for _, k := range []tea.KeyType{tea.KeyDown, tea.KeyUp} {
		m := statusModel{settings: &settingsState{current: store.DefaultSettings(), cursor: 1, saved: true}}
		next, _ := m.Update(tea.KeyMsg{Type: k})
		if next.(statusModel).settings.saved {
			t.Errorf("%v left the saved indicator up", k)
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
