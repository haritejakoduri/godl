package cmd

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"godl/internal/connections"
	"godl/internal/daemon"
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

	// step 2: browsing.
	connName string
	client   *webdav.Client
	path     string // current remote directory, always starting with "/"
	entries  []webdav.Entry
	cursor   int
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
}

type webdavListedMsg struct {
	path    string
	entries []webdav.Entry
}
type webdavListErrMsg struct{ err error }
type webdavStartedMsg struct {
	n   int
	err error
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
// path, all against the same connection and default output directory —
// same as "godl webdav <conn> <path>" run once per selection.
func startWebDAVDownloads(connName string, remotePaths []string) tea.Cmd {
	return func() tea.Msg {
		if err := daemon.EnsureRunning(); err != nil {
			return webdavStartedMsg{err: err}
		}
		def, err := paths.WebDAVDataDir()
		if err != nil {
			return webdavStartedMsg{err: err}
		}
		output, err := resolveOutputPath(def)
		if err != nil {
			return webdavStartedMsg{err: err}
		}
		started := 0
		var firstErr error
		for _, p := range remotePaths {
			_, err := daemon.Call(daemon.Request{
				Cmd:    daemon.CmdAddWebDAV,
				Source: connName + ":" + p,
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
		return webdavStartedMsg{n: started, err: firstErr}
	}
}

// openWebDAVDir navigates to target, using this session's cache if
// target's already been listed (instant, no round-trip) or kicking off
// a fresh PROPFIND otherwise.
func (m statusModel) openWebDAVDir(target string) tea.Cmd {
	wb := m.webdavBrowse
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
			wb.connName = conn.Name
			wb.client = client
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

	// webdavBrowsing
	switch msg.String() {
	case "esc":
		m.webdavBrowse = nil
	case "up", "k":
		if wb.cursor > 0 {
			wb.cursor--
		}
	case "down", "j":
		if wb.cursor < len(wb.entries)-1 {
			wb.cursor++
		}
	case "left", "h", "backspace":
		if !wb.loading && wb.path != "/" {
			return m, m.openWebDAVDir(path.Dir(strings.TrimSuffix(wb.path, "/")))
		}
	case "enter":
		if !wb.loading && wb.cursor < len(wb.entries) {
			if e := wb.entries[wb.cursor]; e.IsDir {
				return m, m.openWebDAVDir(e.Path)
			}
		}
	case " ":
		if !wb.loading && wb.cursor < len(wb.entries) {
			p := wb.entries[wb.cursor].Path
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
		if len(targets) == 0 && !wb.loading && wb.cursor < len(wb.entries) {
			targets = []string{wb.entries[wb.cursor].Path}
		}
		if len(targets) == 0 {
			return m, nil
		}
		connName := wb.connName
		m.webdavBrowse = nil
		m.statusMsg = "starting..."
		return m, startWebDAVDownloads(connName, targets)
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

	switch {
	case wb.loading:
		b.WriteString("Loading...\n")
	case wb.err != "":
		b.WriteString(errStyle.Render(wb.err))
		b.WriteString("\n")
	case len(wb.entries) == 0:
		b.WriteString("(empty folder)\n")
	default:
		start := 0
		if wb.cursor >= webdavBrowseVisible {
			start = wb.cursor - webdavBrowseVisible + 1
		}
		end := min(start+webdavBrowseVisible, len(wb.entries))
		for i := start; i < end; i++ {
			e := wb.entries[i]
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
		if len(wb.entries) > webdavBrowseVisible {
			b.WriteString(helpStyle.Render(fmt.Sprintf("(%d-%d of %d)\n", start+1, end, len(wb.entries))))
		}
	}

	b.WriteString(helpStyle.Render("↑/↓ move  enter open folder  space select  d download selected (or current)  ←/backspace up  esc cancel"))
	return b.String()
}
