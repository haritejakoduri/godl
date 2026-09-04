package cmd

import (
	"context"
	"fmt"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"godl/internal/connections"
	"godl/internal/daemon"
	"godl/internal/paths"
	"godl/internal/player"
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

type webdavListedMsg struct {
	path    string
	entries []webdav.Entry
}
type webdavListErrMsg struct{ err error }
type webdavStartedMsg struct {
	n      int
	output string
	err    error
}

// listWebDAVDir lists one directory's immediate children, dirs first
// then alphabetically. Runs over the network, so it's a tea.Cmd rather
// than something Update calls inline.
func listWebDAVDir(client *webdav.Client, dir string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		entries, err := client.List(ctx, dir)
		if err != nil {
			return webdavListErrMsg{err}
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].IsDir != entries[j].IsDir {
				return entries[i].IsDir
			}
			return entries[i].Path < entries[j].Path
		})
		return webdavListedMsg{path: dir, entries: entries}
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
		output, err := resolveOutputPath(outputDir)
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

// playWebDAVFile streams remotePath directly via client (already
// live in memory with credentials from browsing) — no download
// involved, same as a webdav job's "o" action in the jobs table (see
// status.go's doPlay), just reached straight from the browser instead
// of needing to queue+wait for a download job first.
func playWebDAVFile(client *webdav.Client, remotePath string) tea.Cmd {
	return func() tea.Msg {
		target := client.URLFor(remotePath).String()
		auth := &player.Auth{Username: client.Username, Password: client.Password}
		return actionDoneMsg{player.Play(target, auth)}
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
	return listWebDAVDir(wb.client, target)
}

func (m statusModel) updateWebDAVBrowse(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	wb := m.webdavBrowse

	if wb.step == webdavPickConn {
		switch msg.String() {
		case "up", "k":
			if wb.connIndex > 0 {
				wb.connIndex--
			}
		case "down", "j":
			if wb.connIndex < len(wb.conns)-1 {
				wb.connIndex++
			}
		case "enter":
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
			return m, listWebDAVDir(client, "/")
		case "esc":
			m.webdavBrowse = nil
		}
		return m, nil
	}

	// webdavBrowsing, search prompt focused: keystrokes edit the query
	// live rather than driving navigation/selection.
	if wb.searching {
		switch msg.String() {
		case "esc":
			// Cancel: back to browsing the unfiltered listing.
			wb.searching = false
			wb.query = ""
			wb.cursor = 0
			return m, nil
		case "enter":
			// Confirm: stop capturing keystrokes but keep the filter
			// applied, so ↑/↓/space/d immediately act on the narrowed list.
			wb.searching = false
			wb.cursor = 0
			return m, nil
		default:
			var cmd tea.Cmd
			wb.searchInput, cmd = wb.searchInput.Update(msg)
			wb.query = wb.searchInput.Value()
			wb.cursor = 0
			return m, cmd
		}
	}

	visible := wb.visibleEntries()

	// webdavBrowsing
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
		if !wb.loading && wb.cursor < len(visible) {
			if e := visible[wb.cursor]; e.IsDir {
				return m, m.openWebDAVDir(e.Path)
			}
		}
	case " ":
		if !wb.loading && wb.cursor < len(visible) {
			p := visible[wb.cursor].Path
			if wb.selected[p] {
				delete(wb.selected, p)
			} else {
				wb.selected[p] = true
			}
		}
	case "d":
		var targets []string
		for p := range wb.selected {
			targets = append(targets, p)
		}
		if len(targets) == 0 && !wb.loading && wb.cursor < len(visible) {
			targets = []string{visible[wb.cursor].Path}
		}
		if len(targets) == 0 {
			return m, nil
		}
		connName, outputDir := wb.connName, wb.outputDir
		m.webdavBrowse = nil
		m.statusMsg = "starting..."
		return m, startWebDAVDownloads(connName, outputDir, targets)
	case "D":
		// Downloads the folder currently being browsed, in full — not
		// whatever's under the cursor or individually checked with space.
		// Without this, navigating into a folder to look around and then
		// pressing "d" only grabs the single entry the cursor happens to
		// be on (the first one, right after entering) rather than
		// everything inside, which reads as "it only downloaded the first
		// item" even though the daemon's folder-job download is and
		// always was fully recursive — the gap was that there was no way
		// to target the folder you're standing in, only its children.
		if wb.loading {
			return m, nil
		}
		connName, outputDir, target := wb.connName, wb.outputDir, wb.path
		m.webdavBrowse = nil
		m.statusMsg = "starting..."
		return m, startWebDAVDownloads(connName, outputDir, []string{target})
	case "o":
		// Files only — streaming a directory isn't meaningful. Closes the
		// overlay same as d/D: it's what lets the resulting actionDoneMsg's
		// error (e.g. no player installed) actually reach the screen,
		// since viewWebDAVBrowse doesn't render m.statusMsg while it's
		// showing.
		if wb.loading || wb.cursor >= len(visible) || visible[wb.cursor].IsDir {
			return m, nil
		}
		client, target := wb.client, visible[wb.cursor].Path
		m.webdavBrowse = nil
		m.statusMsg = "starting player..."
		return m, playWebDAVFile(client, target)
	}
	return m, nil
}

// webdavBrowseVisible caps how many entries are shown at once, scrolled
// to keep the cursor in view — a folder with hundreds of files
// shouldn't blow out the terminal.
const webdavBrowseVisible = 15

func (m statusModel) viewWebDAVBrowse() string {
	wb := m.webdavBrowse
	var b strings.Builder

	if wb.step == webdavPickConn {
		b.WriteString(statStyle.Render("Browse WebDAV — pick a connection:"))
		b.WriteString("\n")
		for i, c := range wb.conns {
			cursor := "  "
			if i == wb.connIndex {
				cursor = "> "
			}
			b.WriteString(fmt.Sprintf("%s%s (%s)\n", cursor, c.Name, c.URL))
		}
		b.WriteString(helpStyle.Render("↑/↓ select  enter connect  esc cancel"))
		return b.String()
	}

	b.WriteString(statStyle.Render(fmt.Sprintf("%s:%s  (%d selected)", wb.connName, wb.path, len(wb.selected))))
	b.WriteString("\n")
	b.WriteString(helpStyle.Render("downloading to " + shortenHome(wb.outputDir)))
	b.WriteString("\n")

	if wb.searching {
		b.WriteString("Search: " + wb.searchInput.View())
		b.WriteString("\n")
	} else if wb.query != "" {
		b.WriteString(statStyle.Render(fmt.Sprintf("filter: %q (/ to edit, esc to clear)", wb.query)))
		b.WriteString("\n")
	}

	visible := wb.visibleEntries()

	switch {
	case wb.loading:
		b.WriteString("Loading...\n")
	case wb.err != "":
		b.WriteString(errStyle.Render(wb.err))
		b.WriteString("\n")
	case len(visible) == 0 && wb.query != "":
		b.WriteString("(no matches)\n")
	case len(visible) == 0:
		b.WriteString("(empty folder)\n")
	default:
		start := 0
		if wb.cursor >= webdavBrowseVisible {
			start = wb.cursor - webdavBrowseVisible + 1
		}
		end := min(start+webdavBrowseVisible, len(visible))
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
				size = humanBytes(e.Size)
			}
			b.WriteString(fmt.Sprintf("%s%s %-40s %s\n", cursor, check, truncate(name, 40), size))
		}
		if len(visible) > webdavBrowseVisible {
			b.WriteString(helpStyle.Render(fmt.Sprintf("(%d-%d of %d)\n", start+1, end, len(visible))))
		}
	}

	if wb.searching {
		b.WriteString(helpStyle.Render("type to filter  enter confirm  esc cancel"))
	} else {
		b.WriteString(helpStyle.Render("↑/↓ move  enter open folder  space select  / search  d download selected (or current)  D download this whole folder  o play/stream  ←/backspace up  esc cancel"))
	}
	return b.String()
}

// shortenHome renders p with the user's home directory prefix replaced
// by "~", the way most CLI tools display a path back to the user —
// "/home/alice/Downloads" reads as noise next to "~/Downloads".
// Falls back to p unchanged if the home directory can't be determined
// or isn't actually a prefix of p.
func shortenHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(p, home+string(os.PathSeparator)); ok {
		return "~" + string(os.PathSeparator) + rest
	}
	return p
}
