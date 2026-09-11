package tui

import (
	"context"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"godl/internal/daemon"
	"godl/internal/store"
)

var (
	titleStyle = lipgloss.NewStyle().Bold(true).Padding(0, 1)
	helpStyle  = lipgloss.NewStyle().Faint(true).Padding(1, 1, 0, 1)
	errStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Padding(0, 1)
	statStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Padding(0, 1)

	// Plain ANSI 0-15, not hex: foreground-only, so unlike the Selected
	// style these don't have a contrast problem on a re-themed terminal.
	jobStatusStyles = map[store.JobStatus]lipgloss.Style{
		store.StatusQueued:    lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		store.StatusActive:    lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
		store.StatusPaused:    lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		store.StatusCompleted: lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
		store.StatusFailed:    lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
		store.StatusCanceled:  lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
	}
)

type jobsMsg []*daemon.JobView

type subErrMsg struct{ err error }

type subEndedMsg struct{}

type actionDoneMsg struct{ err error }

// playedMsg reports doPlay's outcome. Not actionDoneMsg, which stays
// silent on success: the player is launched detached, so godl only knows
// it *started*. Naming what was handed to it is the only way a user can
// tell success from "the keypress did nothing".
type playedMsg struct {
	target string // empty when err is set before a target was chosen
	err    error
}

// bulkActionDoneMsg reports an action fired against n jobs at once
// (n==1 for the no-selection case). Counts ok/failed separately so one
// failure among several doesn't hide the rest.
type bulkActionDoneMsg struct {
	n          int
	ok, failed int
	err        error // first error encountered, if failed > 0
}

// Dropped if m.settings is nil by the time they arrive — the overlay was
// closed before the round trip finished.
type settingsLoadedMsg struct {
	settings store.Settings
	err      error
}

type settingsSavedMsg struct {
	settings store.Settings
	err      error
}

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
	width     int // last known terminal width, for responsive column sizing
	height    int // last known terminal height, for sizing full-screen overlays

	// Job IDs checked with space. An action key acts on these when
	// non-empty, otherwise on the row under the cursor.
	selected map[string]bool

	// A pending d/D awaiting y/N: remove is consequential enough not to
	// be a single keypress.
	confirmRemove *pendingRemove

	// The overlays. Each is nil when not showing; at most one is set.
	newJob       *newJobState
	webdavBrowse *webdavBrowseState
	settings     *settingsState
}

type pendingRemove struct {
	jobIDs []string
	purge  bool
}

func newStatusModel() statusModel {
	ctx, cancel := context.WithCancel(context.Background())
	snapCh, errCh := daemon.Subscribe(ctx)

	t := table.New(table.WithColumns(columnsForWidth(0)), table.WithFocused(true), table.WithHeight(15))
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
		selected: map[string]bool{},
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
