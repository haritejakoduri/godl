package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"godl/internal/daemon"
	"godl/internal/format"
	"godl/internal/store"
)

// detailsState is the job details overlay ("i" or enter): the job's
// full source and destination, and for one made of many files (a
// torrent, a WebDAV folder) every file with its own progress.
type detailsState struct {
	jobID   string
	files   []daemon.TorrentFile
	note    string
	err     string
	loaded  bool
	offset  int // first file row shown
	settled bool
}

type detailsLoadedMsg struct {
	st    *detailsState
	files []daemon.TorrentFile
	note  string
	err   error
}

type detailsTickMsg struct{ st *detailsState }

// detailsRefresh is how often an open overlay re-reads the file list —
// only while it's open, and never once a finished job's list is in.
const detailsRefresh = time.Second

func loadDetails(st *detailsState) tea.Cmd {
	return func() tea.Msg {
		resp, err := daemon.Call(daemon.Request{Cmd: daemon.CmdJobFiles, JobID: st.jobID})
		if err != nil {
			return detailsLoadedMsg{st: st, err: err}
		}
		return detailsLoadedMsg{st: st, files: resp.Files, note: resp.Note}
	}
}

func detailsTick(st *detailsState) tea.Cmd {
	return tea.Tick(detailsRefresh, func(time.Time) tea.Msg { return detailsTickMsg{st} })
}

func (m statusModel) openDetails() (tea.Model, tea.Cmd) {
	j, _, ok := m.cursorJob()
	if !ok {
		return m, nil
	}
	m.details = &detailsState{jobID: j.ID}
	return m, loadDetails(m.details)
}

func (m statusModel) detailsJob() *daemon.JobView {
	for _, j := range m.jobs {
		if j.ID == m.details.jobID {
			return j
		}
	}
	return nil
}

func finished(s store.JobStatus) bool {
	return s == store.StatusCompleted || s == store.StatusFailed || s == store.StatusCanceled
}

func (m statusModel) detailsLoaded(msg detailsLoadedMsg) (tea.Model, tea.Cmd) {
	st := m.details
	if st == nil || st != msg.st {
		return m, nil
	}
	st.loaded = true
	if msg.err != nil {
		st.err = msg.err.Error()
	} else {
		st.err, st.files, st.note = "", msg.files, msg.note
	}
	if j := m.detailsJob(); j == nil || finished(j.Status) {
		st.settled = true
		return m, nil
	}
	return m, detailsTick(st)
}

func (m statusModel) updateDetails(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	st := m.details
	page := max(1, m.detailsRows()-1)
	last := max(0, len(st.files)-m.detailsRows())
	switch msg.String() {
	case "esc", "q", "i", "enter":
		m.details = nil
	case "up", "k":
		st.offset = max(0, st.offset-1)
	case "down", "j":
		st.offset = min(last, st.offset+1)
	case "pgup", "b":
		st.offset = max(0, st.offset-page)
	case "pgdown", "f", " ":
		st.offset = min(last, st.offset+page)
	case "home", "g":
		st.offset = 0
	case "end", "G":
		st.offset = last
	}
	return m, nil
}

// detailsHeaderLines is how many lines the overlay uses above and below
// the file rows, so the rows fill whatever the terminal has left.
const detailsHeaderLines = 10

func (m statusModel) detailsRows() int {
	if m.height <= 0 {
		return 15
	}
	return max(3, m.height-detailsHeaderLines)
}

var (
	detailsLabel   = lipgloss.NewStyle().Faint(true)
	detailsSkipped = lipgloss.NewStyle().Faint(true).Strikethrough(true)
)

func (m statusModel) viewDetails() string {
	st := m.details
	j := m.detailsJob()
	var b strings.Builder
	if j == nil {
		b.WriteString(m.wrapped(statStyle).Render("That job is no longer in the list."))
		b.WriteString("\n" + m.helpView("esc close"))
		return b.String()
	}

	name := filepath.Base(j.Output)
	if j.Type == store.JobWebDAV || name == "" || name == "." {
		name = j.Source
	}
	b.WriteString(m.wrapped(titleStyle).Render(name + "  " + jobStatusStyles[j.Status].Render(string(j.Status))))
	b.WriteString("\n")
	line := func(label, value string) {
		b.WriteString(m.wrapped(lipgloss.NewStyle().Padding(0, 1)).Render(detailsLabel.Render(fmt.Sprintf("%-9s", label)) + value))
		b.WriteString("\n")
	}
	line("Source", j.Source)
	line("Saved to", format.ShortenHome(j.Output))
	if j.ErrorMsg != "" && j.Status == store.StatusFailed {
		b.WriteString(m.wrapped(errStyle).Render(j.ErrorMsg))
		b.WriteString("\n")
	}

	switch {
	case !st.loaded:
		b.WriteString(m.wrapped(statStyle).Render("loading the file list..."))
		b.WriteString("\n")
	case st.err != "":
		b.WriteString(m.wrapped(errStyle).Render("error: " + st.err))
		b.WriteString("\n")
	default:
		b.WriteString(m.wrapped(statStyle).Render(detailsSummary(st.files)))
		b.WriteString("\n")
		if st.note != "" {
			b.WriteString(m.wrapped(helpStyle.Padding(0, 1)).Render(st.note))
			b.WriteString("\n")
		}
		b.WriteString(m.detailsTable(j))
	}

	help := "↑/↓ scroll  pgup/pgdn page  esc close"
	if len(st.files) > m.detailsRows() {
		help = fmt.Sprintf("files %d–%d of %d   ", st.offset+1, min(len(st.files), st.offset+m.detailsRows()), len(st.files)) + help
	}
	b.WriteString(m.helpView(help))
	return b.String()
}

// detailsSummary is the line above the file list: how many files, how
// big, how many finished and skipped.
func detailsSummary(files []daemon.TorrentFile) string {
	var total int64
	done, skipped := 0, 0
	for _, f := range files {
		switch {
		case f.Skipped:
			skipped++
			continue
		case f.Length > 0 && f.Done >= f.Length:
			done++
		}
		if f.Length > 0 {
			total += f.Length
		}
	}
	parts := []string{fmt.Sprintf("%d file(s)", len(files))}
	if total > 0 {
		parts = append(parts, format.Bytes(total))
	}
	if len(files) > 1 {
		parts = append(parts, fmt.Sprintf("%d finished", done))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d skipped", skipped))
	}
	return strings.Join(parts, " · ")
}

const detailsBarWidth = 12

func (m statusModel) detailsTable(j *daemon.JobView) string {
	st := m.details
	width := m.width
	if width <= 0 {
		width = 100
	}
	// bar + " 100% " + size column + gaps; the name takes the rest.
	const sizeW = 21
	nameW := max(10, width-2-detailsBarWidth-6-sizeW-2)

	var b strings.Builder
	b.WriteString(detailsLabel.Render(fmt.Sprintf(" %-*s %5s  %*s  %s", detailsBarWidth, "PROGRESS", "", sizeW, "SIZE", "FILE")))
	b.WriteString("\n")
	end := min(len(st.files), st.offset+m.detailsRows())
	for _, f := range st.files[st.offset:end] {
		known := f.Length > 0
		frac := 0.0
		if known {
			frac = float64(f.Done) / float64(f.Length)
		}
		status := store.StatusActive
		if known && f.Done >= f.Length {
			status = store.StatusCompleted
		} else if j.Status != store.StatusActive {
			status = j.Status
		}
		bar, pct, size := renderBar(frac, detailsBarWidth, 0, status, true), fmt.Sprintf("%3.0f%%", frac*100), format.Bytes(f.Length)
		switch {
		case f.Skipped:
			bar, pct = strings.Repeat(" ", detailsBarWidth), "skip"
		case !known:
			bar, pct, size = strings.Repeat("·", detailsBarWidth), "", format.Bytes(f.Done)+" so far"
		case status != store.StatusCompleted:
			size = format.Bytes(f.Done) + " / " + format.Bytes(f.Length)
		}
		name := format.Truncate(f.Path, nameW)
		if f.Skipped {
			name = detailsSkipped.Render(name)
		} else if status == store.StatusCompleted {
			bar = jobStatusStyles[store.StatusCompleted].Render(bar)
		}
		fmt.Fprintf(&b, " %s %5s  %*s  %s\n", bar, pct, sizeW, size, name)
	}
	return b.String()
}
