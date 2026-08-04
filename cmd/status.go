package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/spf13/cobra"

	"godl/internal/daemon"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Full-screen dashboard of all jobs, with live progress, speed, and ETA",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := daemon.EnsureRunning(); err != nil {
			return err
		}
		p := tea.NewProgram(newStatusModel(), tea.WithAltScreen())
		_, err := p.Run()
		return err
	},
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
}

type pendingRemove struct {
	jobID string
	purge bool
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
	styles.Selected = styles.Selected.Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("6"))
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

	case tea.KeyMsg:
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
	if m.statusMsg != "" {
		b.WriteString(statStyle.Render(m.statusMsg))
		b.WriteString("\n")
	}
	b.WriteString(helpStyle.Render("p pause  r resume  x cancel  R retry  d remove  D remove+delete file  ↑/↓ navigate  q quit"))
	return b.String()
}
