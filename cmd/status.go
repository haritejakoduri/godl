package cmd

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/spf13/cobra"

	"godl/internal/connections"
	"godl/internal/daemon"
	"godl/internal/paths"
	"godl/internal/store"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Full-screen dashboard of all jobs, with live progress, speed, and ETA",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStatusTUI()
	},
}

// runStatusTUI launches the full-screen dashboard — shared by "godl
// status" and bare "godl" (see root.go's rootCmd.RunE), so the TUI
// stays a single code path regardless of how it's invoked.
func runStatusTUI() error {
	if err := daemon.EnsureRunning(); err != nil {
		return err
	}
	p := tea.NewProgram(newStatusModel(), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	helpStyle  = lipgloss.NewStyle().Faint(true).Padding(1, 1, 0, 1)
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Padding(0, 1)
	statStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Padding(0, 1)
)

type jobsMsg []*daemon.JobView
type subErrMsg struct{ err error }
type subEndedMsg struct{}
type actionDoneMsg struct{ err error }

type statusModel struct {
	ctx    context.Context
	cancel context.CancelFunc
	snapCh <-chan []*daemon.JobView
	errCh  <-chan error

	table     table.Model
	bar       progress.Model
	jobs      []*daemon.JobView
	err       error
	statusMsg string

	// confirmRemove holds a pending "d"/"D" keypress awaiting a y/N
	// answer — remove (especially with purge) is a step more
	// consequential than pause/cancel/retry, so unlike those it isn't
	// a single keypress.
	confirmRemove *pendingRemove

	// newJob holds in-progress "start a new download" wizard state, or
	// is nil when the overlay isn't showing — another modal interaction
	// alongside confirmRemove, following the same overlay-over-the-
	// existing-table convention rather than a separate full-screen mode.
	newJob *newJobState

	// webdavBrowse holds in-progress "browse a saved WebDAV connection"
	// state, or is nil when the overlay isn't showing — same
	// overlay-over-the-table convention as newJob/confirmRemove.
	webdavBrowse *webdavBrowseState
}

type pendingRemove struct {
	jobID string
	purge bool
}

type newJobStep int

const (
	newJobPickType   newJobStep = iota
	newJobPickPreset            // social only — skipped for url/torrent
	newJobEnterLink
)

type newJobState struct {
	step        newJobStep
	typeIndex   int
	presetIndex int // into socialPresets, social jobs only
	input       textinput.Model
}

// newJobTypes are the job types the TUI can start directly, in the
// order they're offered — mirroring the CLI's url/social/torrent
// subcommands.
var newJobTypes = []struct {
	label string
	cmd   string
}{
	{"URL — direct HTTP(S) link", daemon.CmdAddURL},
	{"Social/media — yt-dlp link", daemon.CmdAddSocial},
	{"Torrent — magnet link or .torrent file", daemon.CmdAddTorrent},
}

func newStatusModel() statusModel {
	ctx, cancel := context.WithCancel(context.Background())
	snapCh, errCh := daemon.Subscribe(ctx)

	columns := []table.Column{
		{Title: "ID", Width: 9},
		{Title: "Type", Width: 8},
		{Title: "Status", Width: 10},
		{Title: "Progress", Width: 24},
		{Title: "Speed", Width: 12},
		{Title: "ETA", Width: 8},
		{Title: "Source", Width: 36},
	}
	t := table.New(table.WithColumns(columns), table.WithFocused(true), table.WithHeight(15))
	styles := table.DefaultStyles()
	styles.Header = styles.Header.Bold(true)
	// Fixed hex, not ANSI palette indices ("0"/"6"): those map to
	// whatever the terminal's own theme has assigned them, and on a lot
	// of terminals (Windows Terminal/PowerShell in particular) a
	// re-themed index 0 can land close enough to index 6 that the
	// selected row's text becomes unreadable. Explicit hex colors
	// render the same guaranteed-contrast pairing everywhere.
	styles.Selected = styles.Selected.Bold(true).Foreground(lipgloss.Color("#0B0B0B")).Background(lipgloss.Color("#5FD6C9"))
	t.SetStyles(styles)

	// bubbles/table truncates cells with go-runewidth, which doesn't parse
	// ANSI escapes — a colored bar gets sliced mid-escape-sequence and
	// corrupts the whole row. Force plain ASCII rendering for the bar.
	bar := progress.New(progress.WithColorProfile(termenv.Ascii), progress.WithWidth(18))

	return statusModel{
		ctx: ctx, cancel: cancel,
		snapCh: snapCh, errCh: errCh,
		table: t, bar: bar,
	}
}

func waitForSnapshot(snapCh <-chan []*daemon.JobView, errCh <-chan error) tea.Cmd {
	return func() tea.Msg {
		select {
		case jobs, ok := <-snapCh:
			if !ok {
				return subEndedMsg{}
			}
			return jobsMsg(jobs)
		case err := <-errCh:
			return subErrMsg{err}
		}
	}
}

func doJobAction(apiCmd, jobID string) tea.Cmd {
	return func() tea.Msg {
		_, err := daemon.Call(daemon.Request{Cmd: apiCmd, JobID: jobID})
		return actionDoneMsg{err}
	}
}

func doRemove(jobID string, purge bool) tea.Cmd {
	return func() tea.Msg {
		_, err := daemon.Call(daemon.Request{Cmd: daemon.CmdRemove, JobID: jobID, Purge: purge})
		return actionDoneMsg{err}
	}
}

// buildAddRequest fills in a daemon.Request for apiCmd (CmdAddURL/
// CmdAddSocial/CmdAddTorrent) from source, applying the same output
// defaults as the corresponding CLI command (see url.go/social.go/
// torrent.go's RunE) — so a download started from the TUI behaves
// exactly like "godl url"/"godl social"/"godl torrent".
func buildAddRequest(apiCmd, source string) (daemon.Request, error) {
	switch apiCmd {
	case daemon.CmdAddURL:
		dir, err := paths.DownloadsDir()
		if err != nil {
			return daemon.Request{}, err
		}
		output, err := resolveOutputPath(filepath.Join(dir, filenameFromURL(source)))
		if err != nil {
			return daemon.Request{}, err
		}
		return daemon.Request{Cmd: apiCmd, Source: source, Output: output, Concurrency: 4}, nil

	case daemon.CmdAddSocial:
		dir, err := paths.DownloadsDir()
		if err != nil {
			return daemon.Request{}, err
		}
		output, err := resolveOutputPath(dir)
		if err != nil {
			return daemon.Request{}, err
		}
		return daemon.Request{Cmd: apiCmd, Source: source, Output: output}, nil

	case daemon.CmdAddTorrent:
		def, err := paths.DownloadsDir()
		if err != nil {
			return daemon.Request{}, err
		}
		output, err := resolveOutputPath(def)
		if err != nil {
			return daemon.Request{}, err
		}
		if !strings.HasPrefix(source, "magnet:") {
			abs, err := resolveOutputPath(source)
			if err != nil {
				return daemon.Request{}, err
			}
			source = abs
		}
		return daemon.Request{Cmd: apiCmd, Source: source, Output: output}, nil

	default:
		return daemon.Request{}, fmt.Errorf("unknown job type %q", apiCmd)
	}
}

// startNewJob starts apiCmd's job for source. format is only meaningful
// for CmdAddSocial (the chosen preset's yt-dlp format selector, or ""
// for the default); buildAddRequest never sets Format for url/torrent,
// and the daemon ignores Format for those job types, so passing it
// through unconditionally is harmless for them.
func startNewJob(apiCmd, source, format string) tea.Cmd {
	return func() tea.Msg {
		if err := daemon.EnsureRunning(); err != nil {
			return actionDoneMsg{err}
		}
		req, err := buildAddRequest(apiCmd, source)
		if err != nil {
			return actionDoneMsg{err}
		}
		req.Format = format
		_, err = daemon.Call(req)
		return actionDoneMsg{err}
	}
}

func (m statusModel) Init() tea.Cmd {
	return waitForSnapshot(m.snapCh, m.errCh)
}

func (m statusModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.table.SetWidth(msg.Width)
		if h := msg.Height - 7; h > 3 {
			m.table.SetHeight(h)
		}
		return m, nil

	case jobsMsg:
		m.jobs = msg
		m.err = nil
		m.rebuildRows()
		return m, waitForSnapshot(m.snapCh, m.errCh)

	case subErrMsg:
		m.err = msg.err
		return m, nil

	case subEndedMsg:
		return m, nil

	case actionDoneMsg:
		if msg.err != nil {
			m.statusMsg = "error: " + msg.err.Error()
		} else {
			m.statusMsg = ""
		}
		return m, nil

	case webdavListedMsg:
		if m.webdavBrowse != nil {
			m.webdavBrowse.loading = false
			m.webdavBrowse.err = ""
			m.webdavBrowse.path = msg.path
			m.webdavBrowse.entries = msg.entries
			m.webdavBrowse.cursor = 0
			m.webdavBrowse.cache[msg.path] = msg.entries
		}
		return m, nil

	case webdavListErrMsg:
		if m.webdavBrowse != nil {
			m.webdavBrowse.loading = false
			m.webdavBrowse.err = msg.err.Error()
		}
		return m, nil

	case webdavStartedMsg:
		switch {
		case msg.err != nil && msg.n > 0:
			m.statusMsg = fmt.Sprintf("started %d download(s), then: %s", msg.n, msg.err.Error())
		case msg.err != nil:
			m.statusMsg = "error: " + msg.err.Error()
		default:
			m.statusMsg = fmt.Sprintf("Started %d download(s) -> %s. Track them with \"godl status\"/\"godl list\".", msg.n, shortenHome(msg.output))
		}
		return m, nil

	case tea.KeyMsg:
		if m.newJob != nil {
			return m.updateNewJob(msg)
		}
		if m.webdavBrowse != nil {
			return m.updateWebDAVBrowse(msg)
		}
		if m.confirmRemove != nil {
			pending := *m.confirmRemove
			m.confirmRemove = nil
			switch msg.String() {
			case "y", "Y":
				m.statusMsg = ""
				return m, doRemove(pending.jobID, pending.purge)
			default:
				m.statusMsg = "remove canceled"
				return m, nil
			}
		}
		switch msg.String() {
		case "q", "ctrl+c":
			m.cancel()
			return m, tea.Quit
		case "n":
			ti := textinput.New()
			ti.Placeholder = "paste a link..."
			ti.Focus()
			ti.CharLimit = 2048
			ti.Width = 60
			m.newJob = &newJobState{step: newJobPickType, input: ti}
			return m, nil
		case "w":
			conns, err := connections.List()
			if err != nil {
				m.statusMsg = "error: " + err.Error()
				return m, nil
			}
			if len(conns) == 0 {
				m.statusMsg = `No saved connections. Run "godl connection add <name> --url ..." first.`
				return m, nil
			}
			m.webdavBrowse = &webdavBrowseState{step: webdavPickConn, conns: conns}
			return m, nil
		case "p", "r", "x", "R":
			row := m.table.SelectedRow()
			if len(row) == 0 {
				return m, nil
			}
			jobID := row[0]
			apiCmd := map[string]string{
				"p": daemon.CmdPause,
				"r": daemon.CmdResume,
				"x": daemon.CmdCancel,
				"R": daemon.CmdRetry,
			}[msg.String()]
			return m, doJobAction(apiCmd, jobID)
		case "d", "D":
			row := m.table.SelectedRow()
			if len(row) == 0 {
				return m, nil
			}
			jobID := row[0]
			purge := msg.String() == "D"
			m.confirmRemove = &pendingRemove{jobID: jobID, purge: purge}
			if purge {
				m.statusMsg = fmt.Sprintf("Remove %s AND DELETE its downloaded file(s)? [y/N]", jobID)
			} else {
				m.statusMsg = fmt.Sprintf("Remove %s from the list (keeps files)? [y/N]", jobID)
			}
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m statusModel) updateNewJob(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.newJob.step {
	case newJobPickType:
		switch msg.String() {
		case "up", "k":
			if m.newJob.typeIndex > 0 {
				m.newJob.typeIndex--
			}
		case "down", "j":
			if m.newJob.typeIndex < len(newJobTypes)-1 {
				m.newJob.typeIndex++
			}
		case "enter":
			if newJobTypes[m.newJob.typeIndex].cmd == daemon.CmdAddSocial {
				m.newJob.step = newJobPickPreset
			} else {
				m.newJob.step = newJobEnterLink
			}
		case "esc":
			m.newJob = nil
		}
		return m, nil

	case newJobPickPreset:
		switch msg.String() {
		case "up", "k":
			if m.newJob.presetIndex > 0 {
				m.newJob.presetIndex--
			}
		case "down", "j":
			if m.newJob.presetIndex < len(socialPresets)-1 {
				m.newJob.presetIndex++
			}
		case "enter":
			m.newJob.step = newJobEnterLink
		case "esc":
			m.newJob.step = newJobPickType
		}
		return m, nil

	default: // newJobEnterLink
		switch msg.String() {
		case "esc":
			if newJobTypes[m.newJob.typeIndex].cmd == daemon.CmdAddSocial {
				m.newJob.step = newJobPickPreset
			} else {
				m.newJob.step = newJobPickType
			}
			return m, nil
		case "enter":
			link := strings.TrimSpace(m.newJob.input.Value())
			if link == "" {
				return m, nil
			}
			apiCmd := newJobTypes[m.newJob.typeIndex].cmd
			format := ""
			if apiCmd == daemon.CmdAddSocial {
				format = socialPresets[m.newJob.presetIndex].Format
			}
			m.newJob = nil
			m.statusMsg = "starting..."
			return m, startNewJob(apiCmd, link, format)
		default:
			var cmd tea.Cmd
			m.newJob.input, cmd = m.newJob.input.Update(msg)
			return m, cmd
		}
	}
}

// selectedJobError returns the currently-selected job's failure reason,
// or "" if it's not failed (or has none recorded) — see View()'s use
// of this for surfacing ErrorMsg without a fixed-width table column.
func (m statusModel) selectedJobError() string {
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.jobs) {
		return ""
	}
	j := m.jobs[idx]
	if j.Status != store.StatusFailed || j.ErrorMsg == "" {
		return ""
	}
	return fmt.Sprintf("%s failed: %s", j.ID, j.ErrorMsg)
}

func (m *statusModel) rebuildRows() {
	rows := make([]table.Row, 0, len(m.jobs))
	for _, j := range m.jobs {
		bar := m.bar.ViewAs(percent(j.BytesDone, j.BytesTotal))
		rows = append(rows, table.Row{
			j.ID,
			string(j.Type),
			string(j.Status),
			bar,
			humanSpeed(j.SpeedBps),
			humanETA(j.ETASeconds, j.Status),
			truncate(j.Source, 36),
		})
	}
	m.table.SetRows(rows)
}

func (m statusModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render(fmt.Sprintf("godl status — %d job(s)", len(m.jobs))))
	b.WriteString("\n")
	b.WriteString(m.table.View())
	b.WriteString("\n")
	if m.err != nil {
		b.WriteString(errStyle.Render("daemon connection error: " + m.err.Error()))
		b.WriteString("\n")
	}
	switch {
	case m.newJob != nil:
		b.WriteString(m.viewNewJob())
	case m.webdavBrowse != nil:
		b.WriteString(m.viewWebDAVBrowse())
	case m.statusMsg != "":
		b.WriteString(statStyle.Render(m.statusMsg))
		b.WriteString("\n")
	case m.selectedJobError() != "":
		// The jobs table's Status column is too narrow to show a failure
		// reason, so a failed row's ErrorMsg shows here instead, just by
		// scrolling to it — no extra keybinding needed.
		b.WriteString(errStyle.Render(m.selectedJobError()))
		b.WriteString("\n")
	}
	b.WriteString(helpStyle.Render("p pause  r resume  x cancel  R retry  d remove  D remove+delete file  n new download  w browse webdav  ↑/↓ navigate  q quit"))
	return b.String()
}

func (m statusModel) viewNewJob() string {
	switch m.newJob.step {
	case newJobPickType:
		var b strings.Builder
		b.WriteString(statStyle.Render("Start a new download — pick a type:"))
		b.WriteString("\n")
		for i, t := range newJobTypes {
			cursor := "  "
			if i == m.newJob.typeIndex {
				cursor = "> "
			}
			b.WriteString(cursor + t.label + "\n")
		}
		b.WriteString(helpStyle.Render("↑/↓ select  enter next  esc cancel"))
		return b.String()

	case newJobPickPreset:
		var b strings.Builder
		b.WriteString(statStyle.Render("Social/media — pick a quality preset:"))
		b.WriteString("\n")
		for i, p := range socialPresets {
			cursor := "  "
			if i == m.newJob.presetIndex {
				cursor = "> "
			}
			b.WriteString(fmt.Sprintf("%s%-8s %s\n", cursor, p.Name, p.Description))
		}
		b.WriteString(helpStyle.Render("↑/↓ select  enter next  esc back"))
		return b.String()

	default: // newJobEnterLink
		label := newJobTypes[m.newJob.typeIndex].label
		if newJobTypes[m.newJob.typeIndex].cmd == daemon.CmdAddSocial {
			label += " [" + socialPresets[m.newJob.presetIndex].Name + "]"
		}
		var b strings.Builder
		b.WriteString(statStyle.Render(fmt.Sprintf("%s — paste the link:", label)))
		b.WriteString("\n")
		b.WriteString(m.newJob.input.View())
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("enter start  esc back"))
		return b.String()
	}
}
