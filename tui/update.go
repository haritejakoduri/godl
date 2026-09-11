package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"godl/internal/connections"
	"godl/internal/daemon"
	"godl/internal/format"
	"godl/internal/store"
)

func (m statusModel) Init() tea.Cmd {
	return waitForSnapshot(m.snapCh, m.errCh)
}

// applyJobs installs a fresh snapshot, keeping the cursor on the job it
// was already pointing at. A newly started job appears at the very top,
// ahead of everything already listed, so a cursor kept by row index
// would silently follow the new arrival instead of the job being
// watched. The row is resolved before rebuildRows (not after, via
// m.table.Cursor()) so rebuildRows skips status-coloring the right row
// the first time it renders post-reorder — see its own doc comment.
func (m statusModel) applyJobs(msg jobsMsg) statusModel {
	prevID := m.cursorJobID()
	m.jobs = newestFirst(msg)
	m.err = nil
	m.pruneSelected()

	cursor := 0
	if prevID != "" {
		for i, j := range m.jobs {
			if j.ID == prevID {
				cursor = i
				break
			}
		}
	}
	m.rebuildRows(cursor)
	m.table.SetCursor(cursor)
	return m
}

// settingsResult applies a reply from the daemon to the Settings tab.
// saved marks the "saved" confirmation, which only a save earns.
func (m statusModel) settingsResult(s store.Settings, err error, saved bool) (tea.Model, tea.Cmd) {
	if m.settings == nil {
		return m, nil // the tab was closed before this reply arrived
	}
	m.settings.loading = false
	m.settings.saved = false
	if err != nil {
		m.settings.err = err.Error()
		return m, nil
	}
	m.settings.current = s
	m.settings.err = ""
	m.settings.saved = saved
	return m, nil
}

func (m statusModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.table.SetColumns(columnsForWidth(msg.Width))
		m.table.SetWidth(msg.Width)
		if h := msg.Height - 7; h > 3 {
			m.table.SetHeight(h)
		}
		return m, nil

	case jobsMsg:
		return m.applyJobs(msg), waitForSnapshot(m.snapCh, m.errCh)

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

	case playedMsg:
		if msg.err != nil {
			m.statusMsg = "error: " + msg.err.Error()
		} else {
			m.statusMsg = "playing " + format.Truncate(format.ShortenHome(msg.target), 70)
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
		if wb := m.webdavBrowse; wb != nil {
			wb.loading, wb.err = false, ""
			wb.path, wb.entries, wb.cursor = msg.path, msg.entries, 0
			wb.cache[msg.path] = msg.entries
		}
		return m, nil

	case webdavListErrMsg:
		if wb := m.webdavBrowse; wb != nil {
			wb.loading, wb.err = false, msg.err.Error()
		}
		return m, nil

	case webdavStartedMsg:
		switch {
		case msg.err != nil && msg.n > 0:
			m.statusMsg = fmt.Sprintf("started %d download(s), then: %s", msg.n, msg.err.Error())
		case msg.err != nil:
			m.statusMsg = "error: " + msg.err.Error()
		default:
			m.statusMsg = fmt.Sprintf("Started %d download(s) -> %s. Track them with \"godl status\"/\"godl list\".", msg.n, format.ShortenHome(msg.output))
		}
		return m, nil

	case settingsLoadedMsg:
		return m.settingsResult(msg.settings, msg.err, false)

	case settingsSavedMsg:
		return m.settingsResult(msg.settings, msg.err, true)

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

// handleKey routes a keypress: to whichever overlay is open, to a
// pending confirmation, or to the dashboard's own bindings. Keys it
// doesn't claim drive the table's cursor.
func (m statusModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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
		return m.resolveRemove(msg)
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
		return m.openWebDAVBrowser()
	case "s":
		m.settings = &settingsState{loading: true}
		return m, loadSettings()
	case " ":
		j, idx, ok := m.cursorJob()
		if !ok {
			return m, nil
		}
		if m.selected[j.ID] {
			delete(m.selected, j.ID)
		} else {
			m.selected[j.ID] = true
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
		return m.promptRemove(msg.String() == "D")
	case "o":
		j, _, ok := m.cursorJob()
		if !ok {
			return m, nil
		}
		m.statusMsg = "starting mpv..."
		return m, doPlay(j)
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

// resolveRemove answers the pending remove confirmation: anything but
// y/Y cancels it.
func (m statusModel) resolveRemove(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
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

// promptRemove arms the y/N confirmation for the current action targets.
// purge additionally deletes the downloaded files.
func (m statusModel) promptRemove(purge bool) (tea.Model, tea.Cmd) {
	ids := m.actionTargets()
	if len(ids) == 0 {
		return m, nil
	}
	m.confirmRemove = &pendingRemove{jobIDs: ids, purge: purge}

	what := fmt.Sprintf("%d jobs", len(ids))
	if len(ids) == 1 {
		what = ids[0]
	}
	if purge {
		m.statusMsg = fmt.Sprintf("Remove %s AND DELETE the downloaded file(s)? [y/N]", what)
	} else {
		m.statusMsg = fmt.Sprintf("Remove %s from the list (keeps files)? [y/N]", what)
	}
	return m, nil
}

func (m statusModel) openWebDAVBrowser() (tea.Model, tea.Cmd) {
	conns, err := connections.List()
	switch {
	case err != nil:
		m.statusMsg = "error: " + err.Error()
	case len(conns) == 0:
		m.statusMsg = `No saved connections. Run "godl connection add <name> --url ..." first.`
	default:
		m.webdavBrowse = &webdavBrowseState{step: webdavPickConn, conns: conns}
	}
	return m, nil
}
