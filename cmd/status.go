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

	// jobStatusStyles color-codes the Status column so a job's state
	// reads at a glance instead of requiring you to read the word:
	// gray for not-currently-running (queued/canceled), amber for
	// paused (idle, but only because someone asked it to be), bright
	// yellow for active (matches statStyle's own "something's
	// happening" color), green for completed, red for failed (matches
	// errStyle). Plain ANSI 0-15 codes, not hex — like errStyle/
	// statStyle above, these are foreground-only (no background), so
	// they don't have the Selected style's contrast-on-a-re-themed-
	// terminal problem that motivated hex there.
	jobStatusStyles = map[store.JobStatus]lipgloss.Style{
		store.StatusQueued:    lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		store.StatusActive:    lipgloss.NewStyle().Foreground(lipgloss.Color("11")),
		store.StatusPaused:    lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		store.StatusCompleted: lipgloss.NewStyle().Foreground(lipgloss.Color("10")),
		store.StatusFailed:    lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
		store.StatusCanceled:  lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
	}
)

// renderStatus color-codes status for the jobs table's Status cell.
// Column width matters here in a way it doesn't for any other cell:
// bubbles/table truncates every cell via go-runewidth, which (same
// trap as the progress bar — see newStatusModel's comment) isn't
// ANSI-aware and overcounts a colored string's width by the escape
// sequences' own byte length, not just its visible characters. Get
// the column width wrong and it truncates mid-escape-sequence,
// corrupting the row. statusColWidth is sized (and verified in
// TestRenderStatusFitsStatusColumn) to comfortably clear that
// overcount for every status word with the colors above, so this
// never happens — unlike the bar, which sidesteps the problem
// entirely by forcing plain ASCII, coloring the actual text is the
// point here, so the fix is a wide-enough column instead.
func renderStatus(status store.JobStatus) string {
	style, ok := jobStatusStyles[status]
	if !ok {
		return string(status)
	}
	return style.Render(string(status))
}

type jobsMsg []*daemon.JobView
type subErrMsg struct{ err error }
type subEndedMsg struct{}
type actionDoneMsg struct{ err error }

// bulkActionDoneMsg reports the outcome of a pause/resume/cancel/retry/
// remove fired against n job IDs at once (n==1 for the ordinary,
// no-selection case — see actionTargets). Unlike actionDoneMsg, one
// failure among several targets doesn't hide the rest: ok/failed are
// counted separately so "3 succeeded, 1 failed" is reported instead of
// only ever the first error swallowing everything else.
type bulkActionDoneMsg struct {
	n          int
	ok, failed int
	err        error // first error encountered, if failed > 0
}

// settingsLoadedMsg/settingsSavedMsg carry the daemon's response to
// get_settings/set_settings back into Update — see loadSettings/
// saveSettings. Both are handled even if m.settings is nil by the time
// they arrive (the overlay was closed before the round trip finished),
// in which case they're just dropped.
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

	// selected holds job IDs checked with space, for a bulk pause/
	// resume/cancel/retry/remove — same convention as webdavBrowseState's
	// own multi-select: an action key with the map non-empty acts on
	// every selected job; with it empty, on just the row under the
	// cursor, same as before multi-select existed.
	selected map[string]bool

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

	// settings holds the Settings tab's state, or is nil when it isn't
	// showing — same overlay-over-the-table convention as newJob/
	// webdavBrowse/confirmRemove.
	settings *settingsState
}

type pendingRemove struct {
	jobIDs []string
	purge  bool
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

// statusColWidth is wider than the Status column strictly needs to be
// for its longest plain word ("completed", 9 chars) — see renderStatus's
// doc comment for why the extra room is load-bearing, not cosmetic.
const statusColWidth = 16

// fixedColumns are every table column except Path and Source, which grow
// or shrink with the terminal width instead of holding a constant size —
// see columnsForWidth. The first column has no header text: it's just
// each row's "[ ]"/"[x]" multi-select checkbox (see rebuildRows/the
// space key), which doesn't need a label to be self-explanatory.
var fixedColumns = []table.Column{
	{Title: "", Width: 3},
	{Title: "ID", Width: 9},
	{Title: "Type", Width: 8},
	{Title: "Status", Width: statusColWidth},
	{Title: "Progress", Width: 24},
	{Title: "Speed", Width: 12},
	{Title: "ETA", Width: 8},
}

// minPathWidth/minSourceWidth are floors for the two variable-width
// columns — narrow enough to still fit in a small terminal without the
// table itself needing to horizontally scroll.
const (
	minPathWidth   = 16
	minSourceWidth = 20
	numColumns     = 9 // fixedColumns + Path + Source
	// bubbles/table pads every cell 1 space on each side (see
	// table.DefaultStyles), on top of the column's own Width.
	perColumnPadding = 2
)

// columnsForWidth builds the table's columns for a terminal width chars
// wide, giving Path and Source (a local filesystem path and a URL/
// magnet/file source respectively — both often too long to fit in a
// fixed-width column without heavy truncation) whatever room is left
// over after the fixed columns and per-cell padding, instead of a
// constant width that either wastes space on a wide terminal or gets
// truncated hard on a narrow one. Source gets more of the extra room
// than Path, since links tend to run longer than local paths.
func columnsForWidth(width int) []table.Column {
	fixedWidth := 0
	for _, c := range fixedColumns {
		fixedWidth += c.Width
	}
	avail := width - fixedWidth - numColumns*perColumnPadding
	pathW, sourceW := minPathWidth, minSourceWidth
	if extra := avail - pathW - sourceW; extra > 0 {
		pathW += extra * 2 / 5
		sourceW += extra - extra*2/5
	}
	cols := make([]table.Column, 0, numColumns)
	cols = append(cols, fixedColumns...)
	cols = append(cols, table.Column{Title: "Path", Width: pathW}, table.Column{Title: "Source", Width: sourceW})
	return cols
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

// doBulkJobAction fires apiCmd (pause/resume/cancel/retry) against
// every job in jobIDs, sequentially — these are lightweight
// control-plane RPCs (not data transfer), so there's no throughput
// reason to parallelize, and sequential keeps the daemon's per-job
// state transitions easy to reason about under a bulk request. See
// bulkActionDoneMsg for how partial failure is reported.
func doBulkJobAction(apiCmd string, jobIDs []string) tea.Cmd {
	return func() tea.Msg {
		var ok, failed int
		var firstErr error
		for _, id := range jobIDs {
			if _, err := daemon.Call(daemon.Request{Cmd: apiCmd, JobID: id}); err != nil {
				failed++
				if firstErr == nil {
					firstErr = err
				}
			} else {
				ok++
			}
		}
		return bulkActionDoneMsg{n: len(jobIDs), ok: ok, failed: failed, err: firstErr}
	}
}

func doBulkRemove(jobIDs []string, purge bool) tea.Cmd {
	return func() tea.Msg {
		var ok, failed int
		var firstErr error
		for _, id := range jobIDs {
			if _, err := daemon.Call(daemon.Request{Cmd: daemon.CmdRemove, JobID: id, Purge: purge}); err != nil {
				failed++
				if firstErr == nil {
					firstErr = err
				}
			} else {
				ok++
			}
		}
		return bulkActionDoneMsg{n: len(jobIDs), ok: ok, failed: failed, err: firstErr}
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
		m.width = msg.Width
		m.table.SetColumns(columnsForWidth(msg.Width))
		m.table.SetWidth(msg.Width)
		if h := msg.Height - 7; h > 3 {
			m.table.SetHeight(h)
		}
		return m, nil

	case jobsMsg:
		prevID := m.cursorJobID()
		m.jobs = newestFirst(msg)
		m.err = nil
		m.pruneSelected()
		// A newly started job appears at the very top, ahead of
		// everything already in the list — without this, the cursor
		// would silently point at whatever new job just landed on the
		// same row index instead of staying on the job actually being
		// looked at. Found before rebuildRows (not after, via
		// m.table.Cursor()) so rebuildRows skips status-coloring the
		// right row the very first time it renders post-reorder — see
		// its own doc comment for why that skip exists at all.
		newCursor := 0
		if prevID != "" {
			for i, j := range m.jobs {
				if j.ID == prevID {
					newCursor = i
					break
				}
			}
		}
		m.rebuildRows(newCursor)
		m.table.SetCursor(newCursor)
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

	case bulkActionDoneMsg:
		switch {
		case msg.n <= 1 && msg.failed == 0:
			// A single, no-selection action stays silent on success —
			// same as before bulk actions existed, so ordinary
			// single-job use isn't suddenly noisier.
			m.statusMsg = ""
		case msg.n <= 1:
			m.statusMsg = "error: " + msg.err.Error()
		case msg.failed == 0:
			m.statusMsg = fmt.Sprintf("%d job(s) updated", msg.ok)
		default:
			m.statusMsg = fmt.Sprintf("%d job(s) updated, %d failed: %s", msg.ok, msg.failed, msg.err.Error())
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

	case settingsLoadedMsg:
		if m.settings == nil {
			return m, nil // tab was closed before this response arrived
		}
		m.settings.loading = false
		if msg.err != nil {
			m.settings.err = msg.err.Error()
			return m, nil
		}
		m.settings.current = msg.settings
		m.settings.err = ""
		return m, nil

	case settingsSavedMsg:
		if m.settings == nil {
			return m, nil
		}
		if msg.err != nil {
			m.settings.err = msg.err.Error()
			m.settings.saved = false
			return m, nil
		}
		m.settings.current = msg.settings
		m.settings.err = ""
		m.settings.saved = true
		return m, nil

	case tea.KeyMsg:
		if m.newJob != nil {
			return m.updateNewJob(msg)
		}
		if m.webdavBrowse != nil {
			return m.updateWebDAVBrowse(msg)
		}
		if m.settings != nil {
			return m.updateSettings(msg)
		}
		if m.confirmRemove != nil {
			pending := *m.confirmRemove
			m.confirmRemove = nil
			switch msg.String() {
			case "y", "Y":
				m.statusMsg = ""
				m.selected = map[string]bool{}
				return m, doBulkRemove(pending.jobIDs, pending.purge)
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
		case "s":
			m.settings = &settingsState{loading: true}
			return m, loadSettings()
		case " ":
			idx := m.table.Cursor()
			if idx < 0 || idx >= len(m.jobs) {
				return m, nil
			}
			id := m.jobs[idx].ID
			if m.selected[id] {
				delete(m.selected, id)
			} else {
				m.selected[id] = true
			}
			m.rebuildRows(idx)
			return m, nil
		case "p", "r", "x", "R":
			ids := m.actionTargets()
			if len(ids) == 0 {
				return m, nil
			}
			apiCmd := map[string]string{
				"p": daemon.CmdPause,
				"r": daemon.CmdResume,
				"x": daemon.CmdCancel,
				"R": daemon.CmdRetry,
			}[msg.String()]
			m.selected = map[string]bool{}
			m.rebuildRows(m.table.Cursor())
			return m, doBulkJobAction(apiCmd, ids)
		case "d", "D":
			ids := m.actionTargets()
			if len(ids) == 0 {
				return m, nil
			}
			purge := msg.String() == "D"
			m.confirmRemove = &pendingRemove{jobIDs: ids, purge: purge}
			switch {
			case len(ids) == 1 && purge:
				m.statusMsg = fmt.Sprintf("Remove %s AND DELETE its downloaded file(s)? [y/N]", ids[0])
			case len(ids) == 1:
				m.statusMsg = fmt.Sprintf("Remove %s from the list (keeps files)? [y/N]", ids[0])
			case purge:
				m.statusMsg = fmt.Sprintf("Remove %d jobs AND DELETE their downloaded file(s)? [y/N]", len(ids))
			default:
				m.statusMsg = fmt.Sprintf("Remove %d jobs from the list (keeps files)? [y/N]", len(ids))
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

// newestFirst returns jobs (store-ordered oldest-created-first) reversed,
// so the dashboard reads top-to-bottom the way you'd expect a live feed
// to: whatever you just started is right there, not pushed to the
// bottom behind everything already running.
func newestFirst(jobs []*daemon.JobView) []*daemon.JobView {
	out := make([]*daemon.JobView, len(jobs))
	for i, j := range jobs {
		out[len(jobs)-1-i] = j
	}
	return out
}

// cursorJobID returns the ID of the job currently under the cursor, or
// "" if there isn't one — used to re-find and re-focus the same job
// after a snapshot reorders the list (see the jobsMsg handler).
func (m statusModel) cursorJobID() string {
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.jobs) {
		return ""
	}
	return m.jobs[idx].ID
}

// pruneSelected drops any selected job ID that's no longer in the
// current snapshot (removed by this client or another) — otherwise a
// stale ID lingers in the map forever, harmlessly but pointlessly.
func (m *statusModel) pruneSelected() {
	if len(m.selected) == 0 {
		return
	}
	live := make(map[string]bool, len(m.jobs))
	for _, j := range m.jobs {
		live[j.ID] = true
	}
	for id := range m.selected {
		if !live[id] {
			delete(m.selected, id)
		}
	}
}

// actionTargets returns the job IDs a pause/resume/cancel/retry/remove
// keypress should act on: every checked job if any are selected,
// otherwise just whatever's under the cursor — the same "selected, or
// current" convention webdavBrowseState's own multi-select already
// uses for its "d" (download) key.
func (m statusModel) actionTargets() []string {
	if len(m.selected) > 0 {
		ids := make([]string, 0, len(m.selected))
		for id := range m.selected {
			ids = append(ids, id)
		}
		return ids
	}
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.jobs) {
		return nil
	}
	return []string{m.jobs[idx].ID}
}

// rebuildRows rebuilds the table's rows from m.jobs. cursorIdx is which
// row index is (about to be) the cursor row: that one renders Status
// as plain text rather than through renderStatus.
//
// This isn't a style choice — raw ANSI SGR codes don't nest through
// plain string concatenation the way markup would. renderStatus's
// closing sequence resets state unconditionally, so if a colored
// Status cell ends up inside the Selected style's row-wide wrapper
// (bubbles/table renders the whole joined row, then wraps *that* in
// Selected for the cursor's row), that reset kills the Selected
// style's bold/background for every cell after Status too — the
// cursor row's highlight visibly "cuts off" partway through instead
// of spanning the row. The cursor row's own highlight already marks
// it unambiguously, so skipping the redundant status color there
// costs nothing and sidesteps the corruption entirely rather than
// fighting raw ANSI nesting to preserve it.
func (m *statusModel) rebuildRows(cursorIdx int) {
	rows := make([]table.Row, 0, len(m.jobs))
	for i, j := range m.jobs {
		bar := m.bar.ViewAs(percent(j.BytesDone, j.BytesTotal))
		check := "[ ]"
		if m.selected[j.ID] {
			check = "[x]"
		}
		status := string(j.Status)
		if i != cursorIdx {
			status = renderStatus(j.Status)
		}
		rows = append(rows, table.Row{
			check,
			j.ID,
			string(j.Type),
			status,
			bar,
			humanSpeed(j.SpeedBps),
			humanETA(j.ETASeconds, j.Status),
			shortenHome(j.Output),
			shortenHome(j.Source),
		})
	}
	m.table.SetRows(rows)
}

func (m statusModel) View() string {
	var b strings.Builder
	title := fmt.Sprintf("godl status — %d job(s)", len(m.jobs))
	if len(m.selected) > 0 {
		title += fmt.Sprintf("  (%d selected)", len(m.selected))
	}
	b.WriteString(titleStyle.Render(title))
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
	case m.settings != nil:
		b.WriteString(m.viewSettings())
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
	b.WriteString(helpStyle.Render("space select  p pause  r resume  x cancel  R retry  d remove  D remove+delete file  n new download  w browse webdav  s settings  ↑/↓ navigate  q quit"))
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
