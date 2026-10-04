package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"godl/internal/daemon"
	"godl/internal/format"
	"godl/internal/paths"
	"godl/internal/torrentmgr"
)

// torrentPick is the new-download wizard's file picker for a torrent:
// every file in it, all ticked to start with, for choosing which to
// download — the TUI's "godl torrent --files".
type torrentPick struct {
	source  string // the magnet or absolute .torrent path being listed
	loading bool
	err     string
	name    string
	files   []daemon.TorrentFile
	sel     []bool
	cursor  int
	offset  int
}

type torrentFilesMsg struct {
	pick  *torrentPick
	name  string
	files []daemon.TorrentFile
	err   error
}

// listTorrentFiles asks the daemon for a torrent's files. For a magnet
// that means fetching its metadata from peers, which can take a while —
// the picker says so, and enter skips waiting (download everything).
func listTorrentFiles(p *torrentPick) tea.Cmd {
	return func() tea.Msg {
		if err := daemon.EnsureRunning(); err != nil {
			return torrentFilesMsg{pick: p, err: err}
		}
		resp, err := daemon.Call(daemon.Request{Cmd: daemon.CmdTorrentFiles, Source: p.source})
		if err != nil {
			return torrentFilesMsg{pick: p, err: err}
		}
		return torrentFilesMsg{pick: p, name: resp.Name, files: resp.Files}
	}
}

// newTorrentPick starts listing link's files.
func newTorrentPick(link string) (*torrentPick, tea.Cmd) {
	source := link
	if !strings.HasPrefix(link, "magnet:") {
		if abs, err := paths.ResolveOutput(link); err == nil {
			source = abs
		}
	}
	p := &torrentPick{source: source, loading: true}
	return p, listTorrentFiles(p)
}

func (m statusModel) torrentFilesLoaded(msg torrentFilesMsg) (tea.Model, tea.Cmd) {
	nj := m.newJob
	if nj == nil || nj.pick != msg.pick || nj.step != newJobPickFiles {
		return m, nil // the wizard moved on or closed meanwhile
	}
	p := nj.pick
	p.loading = false
	if msg.err != nil {
		p.err = msg.err.Error()
		return m, nil
	}
	p.name, p.files = msg.name, msg.files
	p.sel = make([]bool, len(msg.files))
	for i := range p.sel {
		p.sel[i] = true
	}
	// One file leaves nothing to choose.
	if len(p.files) <= 1 {
		return m.advanceToOutput(), nil
	}
	return m, nil
}

// spec is the --files value for the current choice ("" = everything).
func (p *torrentPick) spec() string {
	if p == nil || p.loading || len(p.sel) == 0 {
		return ""
	}
	return torrentmgr.SelectionSpec(p.sel)
}

func (p *torrentPick) counts() (n int, size int64) {
	for i, s := range p.sel {
		if s {
			n++
			size += p.files[i].Length
		}
	}
	return n, size
}

func (m statusModel) pickRows() int {
	if m.height <= 0 {
		return 15
	}
	return max(3, m.height-8)
}

func (m statusModel) updateTorrentPick(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	nj := m.newJob
	p := nj.pick
	switch msg.String() {
	case "esc":
		nj.pick = nil
		nj.step = newJobEnterLink
		return m, nil
	}
	if p.loading || p.err != "" {
		if msg.String() == "enter" {
			// Don't wait for (or retry) the list: download every file.
			nj.pick = nil
			return m.advanceToOutput(), nil
		}
		return m, nil
	}

	rows := m.pickRows()
	switch msg.String() {
	case "up", "k":
		p.cursor = max(0, p.cursor-1)
	case "down", "j":
		p.cursor = min(len(p.files)-1, p.cursor+1)
	case "pgup":
		p.cursor = max(0, p.cursor-rows)
	case "pgdown":
		p.cursor = min(len(p.files)-1, p.cursor+rows)
	case "home", "g":
		p.cursor = 0
	case "end", "G":
		p.cursor = len(p.files) - 1
	case " ", "x":
		p.sel[p.cursor] = !p.sel[p.cursor]
	case "a":
		for i := range p.sel {
			p.sel[i] = true
		}
	case "n":
		for i := range p.sel {
			p.sel[i] = false
		}
	case "enter":
		if n, _ := p.counts(); n == 0 {
			nj.err = "pick at least one file (a selects all)"
			return m, nil
		}
		nj.err = ""
		return m.advanceToOutput(), nil
	}
	nj.err = ""
	if p.cursor < p.offset {
		p.offset = p.cursor
	}
	if p.cursor >= p.offset+rows {
		p.offset = p.cursor - rows + 1
	}
	return m, nil
}

func (m statusModel) viewTorrentPick() string {
	nj := m.newJob
	p := nj.pick
	var b strings.Builder
	switch {
	case p.loading:
		what := "Reading the torrent's file list..."
		if strings.HasPrefix(p.source, "magnet:") {
			what = "Getting the file list from peers (can take up to a minute)..."
		}
		b.WriteString(m.wrapped(statStyle).Render(what))
		b.WriteString("\n")
		b.WriteString(m.helpView("enter download every file without waiting  esc back"))
		return b.String()
	case p.err != "":
		b.WriteString(m.wrapped(errStyle).Render("Couldn't list the torrent's files: " + p.err))
		b.WriteString("\n")
		b.WriteString(m.helpView("enter download every file anyway  esc back"))
		return b.String()
	}

	n, size := p.counts()
	b.WriteString(m.wrapped(statStyle).Render(fmt.Sprintf("%s — choose files: %d of %d selected, %s", p.name, n, len(p.files), format.Bytes(size))))
	b.WriteString("\n")
	width := m.width
	if width <= 0 {
		width = 100
	}
	nameW := max(10, width-2-4-11-1)
	end := min(len(p.files), p.offset+m.pickRows())
	for i := p.offset; i < end; i++ {
		f := p.files[i]
		cursor := "  "
		if i == p.cursor {
			cursor = "> "
		}
		check := "[ ]"
		if p.sel[i] {
			check = "[x]"
		}
		fmt.Fprintf(&b, "%s%s %10s %s\n", cursor, check, format.Bytes(f.Length), format.Truncate(f.Path, nameW))
	}
	if len(p.files) > m.pickRows() {
		b.WriteString(m.helpView(fmt.Sprintf("(%d-%d of %d)", p.offset+1, end, len(p.files))))
		b.WriteString("\n")
	}
	if nj.err != "" {
		b.WriteString(m.wrapped(errStyle).Render(nj.err))
		b.WriteString("\n")
	}
	b.WriteString(m.helpView("space toggle  a select all  n select none  enter next  esc back"))
	return b.String()
}
