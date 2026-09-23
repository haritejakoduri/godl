package tui

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"godl/internal/connections"
	"godl/internal/daemon"
	"godl/internal/format"
	"godl/internal/mpv"
	"godl/internal/paths"
	"godl/internal/webdav"
)

type webdavBrowseStep int

const (
	webdavPickConn webdavBrowseStep = iota
	webdavBrowsing
)

// webdavBrowseState is the TUI's "browse a saved WebDAV connection"
// overlay: pick a connection, then navigate its directory tree
// (PROPFIND per directory, on demand) and queue one or more files/
// folders for download — mirroring what "godl webdav" does from the
// command line, but with live navigation and multi-select instead of a
// single fixed remote-path argument.
type webdavBrowseState struct {
	step webdavBrowseStep

	// step 1: pick a saved connection.
	conns     []connections.Connection
	connIndex int
	// form is non-nil while the "add a connection" form (opened with
	// 'a') is showing, in place of the connection list.
	form *connFormState
	// confirmRemoveConn holds the name of a connection awaiting a y/N
	// removal confirmation (opened with 'd'/'x'), "" when none pending.
	confirmRemoveConn string
	// connMsg is a one-shot status line for the connection list (e.g.
	// "saved connection ..."/"removed connection ..."), cleared on the
	// next keypress the way the dashboard's own statusMsg is.
	connMsg string

	// step 2: browsing.
	connName  string
	client    *webdav.Client
	outputDir string // local destination downloads from this session land in
	path      string // current remote directory, always starting with "/"
	entries   []webdav.Entry
	cursor    int
	// selected holds entry paths queued for bulk download — both files
	// and folders can be selected together, since each becomes its own
	// daemon job regardless (a folder's job downloads it recursively).
	selected map[string]bool
	// cache holds every directory listing already fetched this browse
	// session, keyed by path — revisiting a folder (going back up, then
	// back down, a common navigation pattern) is then instant instead
	// of a fresh PROPFIND round-trip. Session-scoped only: it's
	// discarded with the rest of webdavBrowseState on esc, so a folder
	// that changes on the server mid-session is picked up next time the
	// browser is opened, not stale forever.
	cache   map[string][]webdav.Entry
	loading bool
	pending string // directory the in-flight listing is for, while loading
	err     string

	// searching is true while the "/" search prompt is focused and
	// capturing keystrokes; query is the live filter text (kept even
	// after leaving the prompt with enter, so the filtered view stays
	// up until esc clears it or the user navigates to a new directory —
	// see openWebDAVDir). Matching is a case-insensitive substring
	// against each entry's own name, not its full remote path.
	searching   bool
	query       string
	searchInput textinput.Model
}

// visibleEntries returns wb.entries narrowed to wb.query, or every entry
// unfiltered when there's no active search — the single source every
// cursor/selection/navigation/render operation reads through, so the
// cursor always lines up with what's actually on screen.
func (wb *webdavBrowseState) visibleEntries() []webdav.Entry {
	if wb.query == "" {
		return wb.entries
	}
	q := strings.ToLower(wb.query)
	out := make([]webdav.Entry, 0, len(wb.entries))
	for _, e := range wb.entries {
		name := strings.ToLower(path.Base(strings.TrimSuffix(e.Path, "/")))
		if strings.Contains(name, q) {
			out = append(out, e)
		}
	}
	return out
}

// The listing replies carry the browser session (wb) and directory they
// were requested for. A reply is applied only if it still matches what
// the browser is waiting on: closing and reopening the browser, or
// moving on to another folder mid-load, must not let a late answer
// overwrite the view with a listing the user has already left.
type webdavListedMsg struct {
	wb      *webdavBrowseState
	path    string
	entries []webdav.Entry
}
type webdavListErrMsg struct {
	wb   *webdavBrowseState
	path string
	err  error
}
type webdavStartedMsg struct {
	n      int
	output string
	err    error
}

// listWebDAVDir lists one directory's immediate children, dirs first
// then alphabetically. Runs over the network, so it's a tea.Cmd rather
// than something Update calls inline.
func listWebDAVDir(wb *webdavBrowseState, dir string) tea.Cmd {
	client := wb.client
	return func() tea.Msg {
		// Longer than internal/webdav's own propfindTimeout (3 minutes):
		// that's what actually bounds a single PROPFIND round-trip,
		// including any 429 retries on a rate-limiting backend like
		// TorBox — a shorter timeout here would just cut it off
		// mid-retry, undoing that protection. This one only exists as a
		// backstop so browsing can't hang forever if something upstream
		// changes.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute+10*time.Second)
		defer cancel()
		entries, err := client.List(ctx, dir)
		if err != nil {
			return webdavListErrMsg{wb: wb, path: dir, err: err}
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsDir != entries[j].IsDir {
				return entries[i].IsDir
			}
			return entries[i].Path < entries[j].Path
		})
		return webdavListedMsg{wb: wb, path: dir, entries: entries}
	}
}

// startWebDAVDownloads queues one background daemon job per remote
// path, all against the same connection and outputDir — same as
// "godl webdav <conn> <path> -o <outputDir>" run once per selection.
func startWebDAVDownloads(connName, outputDir string, remotePaths []string) tea.Cmd {
	return func() tea.Msg {
		if err := daemon.EnsureRunning(); err != nil {
			return webdavStartedMsg{err: err}
		}
		output, err := paths.ResolveOutput(outputDir)
		if err != nil {
			return webdavStartedMsg{err: err}
		}
		started := 0
		var firstErr error
		for _, p := range remotePaths {
			_, err := daemon.Call(daemon.Request{
				Cmd:    daemon.CmdAddWebDAV,
				Source: daemon.JoinWebDAVSource(connName, p),
				Output: output,
			})
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			started++
		}
		return webdavStartedMsg{n: started, output: output, err: firstErr}
	}
}

// openWebDAVDir navigates to target, using this session's cache if
// target's already been listed (instant, no round-trip) or kicking off
// a fresh PROPFIND otherwise.
func (m statusModel) openWebDAVDir(target string) tea.Cmd {
	wb := m.webdavBrowse
	// A search filtered to one directory's contents rarely still makes
	// sense in a different one — clear it on every navigation rather
	// than carrying it along and silently hiding entries the user has
	// no reason to expect are filtered.
	wb.searching = false
	wb.query = ""
	if cached, ok := wb.cache[target]; ok {
		wb.path = target
		wb.entries = cached
		wb.cursor = 0
		wb.err = ""
		return nil
	}
	wb.loading = true
	wb.pending = target
	return listWebDAVDir(wb, target)
}

func (m statusModel) updateWebDAVBrowse(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch wb := m.webdavBrowse; {
	case wb.step == webdavPickConn:
		return m.webdavPickConnKey(msg)
	case wb.searching:
		return m.webdavSearchKey(msg)
	default:
		return m.webdavBrowsingKey(msg)
	}
}

func (m statusModel) webdavPickConnKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	wb := m.webdavBrowse
	if wb.form != nil {
		return m.updateConnForm(msg)
	}
	if wb.confirmRemoveConn != "" {
		return m.resolveRemoveConn(msg)
	}
	wb.connMsg = ""
	switch msg.String() {
	case "up", "k":
		if wb.connIndex > 0 {
			wb.connIndex--
		}
	case "down", "j":
		if wb.connIndex < len(wb.conns)-1 {
			wb.connIndex++
		}
	case "a":
		wb.form = newConnForm()
	case "d", "x":
		if len(wb.conns) > 0 {
			wb.confirmRemoveConn = wb.conns[wb.connIndex].Name
		}
	case "enter":
		if len(wb.conns) == 0 {
			return m, nil
		}
		conn := wb.conns[wb.connIndex]
		client, err := webdav.New(conn.URL, conn.Username, conn.Password, conn.Insecure)
		if err != nil {
			m.webdavBrowse = nil
			m.statusMsg = "error: " + err.Error()
			return m, nil
		}
		outputDir, err := paths.DownloadsDir()
		if err != nil {
			m.webdavBrowse = nil
			m.statusMsg = "error: " + err.Error()
			return m, nil
		}
		wb.connName = conn.Name
		wb.client = client
		wb.outputDir = outputDir
		wb.selected = map[string]bool{}
		wb.cache = map[string][]webdav.Entry{}
		wb.step = webdavBrowsing
		wb.loading = true
		wb.pending = "/"
		return m, listWebDAVDir(wb, "/")
	case "esc":
		m.webdavBrowse = nil
	}
	return m, nil
}

// resolveRemoveConn answers the pending y/N connection-removal
// confirmation armed by 'd'/'x' in webdavPickConnKey — mirrors
// resolveRemove's own y/N handling for job removal.
func (m statusModel) resolveRemoveConn(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	wb := m.webdavBrowse
	name := wb.confirmRemoveConn
	wb.confirmRemoveConn = ""
	switch msg.String() {
	case "y", "Y":
		if err := connections.Remove(name); err != nil {
			wb.connMsg = "error: " + err.Error()
			return m, nil
		}
		conns, err := connections.List()
		if err != nil {
			m.webdavBrowse = nil
			m.statusMsg = "error: " + err.Error()
			return m, nil
		}
		wb.conns = conns
		if wb.connIndex >= len(conns) && wb.connIndex > 0 {
			wb.connIndex = len(conns) - 1
		}
		wb.connMsg = fmt.Sprintf("removed connection %q", name)
	default:
		wb.connMsg = "remove canceled"
	}
	return m, nil
}

// webdavSearchKey handles keystrokes while the search prompt is focused:
// they edit the query live rather than driving navigation/selection.
func (m statusModel) webdavSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	wb := m.webdavBrowse
	wb.cursor = 0
	switch msg.String() {
	case "esc":
		// Cancel: back to browsing the unfiltered listing.
		wb.searching = false
		wb.query = ""
	case "enter":
		// Confirm: stop capturing keystrokes but keep the filter applied,
		// so up/down/space/d immediately act on the narrowed list.
		wb.searching = false
	default:
		var cmd tea.Cmd
		wb.searchInput, cmd = wb.searchInput.Update(msg)
		wb.query = wb.searchInput.Value()
		return m, cmd
	}
	return m, nil
}

func (m statusModel) webdavBrowsingKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	wb := m.webdavBrowse
	visible := wb.visibleEntries()
	// Entry under the cursor, if the listing is settled and non-empty.
	current := func() (webdav.Entry, bool) {
		if wb.loading || wb.cursor >= len(visible) {
			return webdav.Entry{}, false
		}
		return visible[wb.cursor], true
	}

	switch msg.String() {
	case "esc":
		m.webdavBrowse = nil
	case "/":
		if wb.loading {
			return m, nil
		}
		ti := textinput.New()
		ti.Placeholder = "search filenames..."
		ti.SetValue(wb.query)
		ti.CursorEnd()
		ti.Focus()
		ti.CharLimit = 200
		ti.Width = 40
		wb.searchInput = ti
		wb.searching = true
	case "up", "k":
		if wb.cursor > 0 {
			wb.cursor--
		}
	case "down", "j":
		if wb.cursor < len(visible)-1 {
			wb.cursor++
		}
	case "left", "h", "backspace":
		if !wb.loading && wb.path != "/" {
			return m, m.openWebDAVDir(path.Dir(strings.TrimSuffix(wb.path, "/")))
		}
	case "enter":
		if e, ok := current(); ok && e.IsDir {
			return m, m.openWebDAVDir(e.Path)
		}
	case " ":
		if e, ok := current(); ok {
			if wb.selected[e.Path] {
				delete(wb.selected, e.Path)
			} else {
				wb.selected[e.Path] = true
			}
		}
	case "d":
		var targets []string
		for p := range wb.selected {
			targets = append(targets, p)
		}
		sort.Strings(targets) // queue in a repeatable order, not map order
		if len(targets) == 0 {
			if e, ok := current(); ok {
				targets = []string{e.Path}
			}
		}
		if len(targets) == 0 {
			return m, nil
		}
		return m.startBrowseDownloads(targets)
	case "o":
		// Files only — there's nothing to stream for a directory. Closes
		// the browser the way d/D do, because viewWebDAVBrowse doesn't
		// render m.statusMsg: leaving it open would swallow the result,
		// including "no media player found".
		e, ok := current()
		if !ok || e.IsDir {
			return m, nil
		}
		return m.startBrowsePlay(e.Path)
	case "D":
		// Downloads the folder currently being browsed, in full — not
		// whatever's under the cursor or individually checked with space.
		// Without this there's no way to target the folder you're standing
		// in, only its children, so "d" right after entering a folder grabs
		// just the first entry and reads as "it only downloaded one item".
		if wb.loading {
			return m, nil
		}
		return m.startBrowseDownloads([]string{wb.path})
	}
	return m, nil
}

// startBrowsePlay closes the browser and streams one remote file
// straight from the server — no download job, no waiting for one to
// finish, the same as a webdav job's "o" in the jobs table (see
// play.go's doPlay) reached one step earlier.
func (m statusModel) startBrowsePlay(remotePath string) (tea.Model, tea.Cmd) {
	client := m.webdavBrowse.client
	m.webdavBrowse = nil
	m.statusMsg = "starting player..."
	return m, func() tea.Msg {
		target := client.URLFor(remotePath).String()
		auth := &mpv.Auth{Username: client.Username, Password: client.Password}
		return playedMsg{target: target, err: mpv.Play(target, auth)}
	}
}

// startBrowseDownloads closes the browser and queues targets for download.
func (m statusModel) startBrowseDownloads(targets []string) (tea.Model, tea.Cmd) {
	wb := m.webdavBrowse
	connName, outputDir := wb.connName, wb.outputDir
	m.webdavBrowse = nil
	m.statusMsg = "starting..."
	return m, startWebDAVDownloads(connName, outputDir, targets)
}

// webdavBrowseVisibleFallback is used before the first WindowSizeMsg
// arrives (m.height still zero) — a reasonable guess rather than
// showing nothing.
const webdavBrowseVisibleFallback = 15

// webdavBrowseMinVisible is the fewest entries worth showing; on a
// terminal too short for even that the view overflows rather than
// showing a uselessly small window.
const webdavBrowseMinVisible = 5

// webdavBrowseVisible is how many entries fit between head and foot,
// scrolled to keep the cursor in view — a folder with hundreds of files
// shouldn't blow out the terminal. It's measured from the head and foot
// as actually rendered (they wrap on a narrow terminal, and the head
// gains a line while searching), not from a constant, so the list uses
// exactly the room the screen has and the title stays on screen.
func (m statusModel) webdavBrowseVisible(head, foot string) int {
	if m.height <= 0 {
		return webdavBrowseVisibleFallback
	}
	// Reserved whether or not it ends up shown, so the list doesn't
	// change size when a folder crosses the one-screen threshold.
	position := lipgloss.Height(m.helpView(browsePosition(1, 1, 1)))
	const slack = 1
	return max(m.height-lipgloss.Height(head)-lipgloss.Height(foot)-position-slack, webdavBrowseMinVisible)
}

func browsePosition(start, end, total int) string {
	return fmt.Sprintf("(%d-%d of %d)", start, end, total)
}

func (m statusModel) viewWebDAVBrowse() string {
	wb := m.webdavBrowse
	var b strings.Builder

	if wb.step == webdavPickConn {
		if wb.form != nil {
			return m.viewConnForm()
		}
		b.WriteString(m.wrapped(statStyle).Render("Browse WebDAV — pick a connection:"))
		b.WriteString("\n")
		if len(wb.conns) == 0 {
			b.WriteString("No saved connections yet. Press a to add one.\n")
		}
		for i, c := range wb.conns {
			cursor := "  "
			if i == wb.connIndex {
				cursor = "> "
			}
			b.WriteString(fmt.Sprintf("%s%s (%s)\n", cursor, c.Name, c.URL))
		}
		switch {
		case wb.confirmRemoveConn != "":
			b.WriteString(m.wrapped(statStyle).Render(fmt.Sprintf("Remove connection %q? [y/N]", wb.confirmRemoveConn)))
			b.WriteString("\n")
		case wb.connMsg != "":
			b.WriteString(m.wrapped(statStyle).Render(wb.connMsg))
			b.WriteString("\n")
		}
		b.WriteString(m.helpView("↑/↓ select  enter connect  a add connection  d remove  esc cancel"))
		return b.String()
	}

	head := m.wrapped(statStyle).Render(fmt.Sprintf("%s:%s  (%d selected)", wb.connName, wb.path, len(wb.selected))) +
		"\n" + m.helpView("downloading to "+format.ShortenHome(wb.outputDir))
	if wb.searching {
		head += "\nSearch: " + wb.searchInput.View()
	} else if wb.query != "" {
		head += "\n" + m.wrapped(statStyle).Render(fmt.Sprintf("filter: %q (/ to edit, esc to clear)", wb.query))
	}

	foot := m.helpView("↑/↓ move  enter open folder  space select  / search  d download selected (or current)  D download this whole folder  o play/stream  ←/backspace up  esc cancel")
	if wb.searching {
		foot = m.helpView("type to filter  enter confirm  esc cancel")
	}

	b.WriteString(head)
	b.WriteString("\n")

	visible := wb.visibleEntries()

	switch {
	case wb.loading:
		b.WriteString("Loading...\n")
	case wb.err != "":
		b.WriteString(m.wrapped(errStyle).Render(wb.err))
		b.WriteString("\n")
	case len(visible) == 0 && wb.query != "":
		b.WriteString("(no matches)\n")
	case len(visible) == 0:
		b.WriteString("(empty folder)\n")
	default:
		visibleRows := m.webdavBrowseVisible(head, foot)
		start := 0
		if wb.cursor >= visibleRows {
			start = wb.cursor - visibleRows + 1
		}
		end := min(start+visibleRows, len(visible))
		for i := start; i < end; i++ {
			e := visible[i]
			cursor := "  "
			if i == wb.cursor {
				cursor = "> "
			}
			check := "[ ]"
			if wb.selected[e.Path] {
				check = "[x]"
			}
			name := path.Base(strings.TrimSuffix(e.Path, "/"))
			size := "-"
			if e.IsDir {
				name += "/"
			} else if e.Size >= 0 {
				size = format.Bytes(e.Size)
			}
			b.WriteString(cursor + check + " " + fitCells(name, m.browseNameWidth()) + " " + size + "\n")
		}
		if len(visible) > visibleRows {
			b.WriteString(m.helpView(browsePosition(start+1, end, len(visible))))
			b.WriteString("\n")
		}
	}

	b.WriteString(foot)
	return b.String()
}

// browseNameWidth is the width, in terminal cells, of the name column:
// up to browseNameMax, shrinking on a narrow terminal so the size column
// isn't pushed off the edge.
func (m statusModel) browseNameWidth() int {
	if m.width <= 0 {
		return browseNameMax
	}
	// cursor (2) + checkbox and its space (4) + the gap and a size like
	// "1023.9 MiB" (11) are what share the line with the name.
	const otherCells = 2 + 4 + 11
	return min(max(m.width-otherCells, browseNameMin), browseNameMax)
}

const (
	browseNameMax = 40
	browseNameMin = 12
)
