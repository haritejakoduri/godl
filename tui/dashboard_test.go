package tui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/muesli/termenv"

	"godl/internal/daemon"
	"godl/internal/store"
)

func dashboardModel(jobs ...*daemon.JobView) statusModel {
	_, cancel := context.WithCancel(context.Background())
	m := statusModel{
		cancel:   cancel,
		table:    table.New(table.WithColumns(columnsForWidth(0)), table.WithFocused(true)),
		bar:      progress.New(progress.WithColorProfile(termenv.Ascii), progress.WithWidth(18)),
		selected: map[string]bool{},
		jobs:     jobs,
	}
	m.rebuildRows(-1)
	return m
}

// TestSubErrMsgKeepsListening: SubscribeRetrying reconnects by itself, so
// an error must not end the model's wait for snapshots — otherwise the
// dashboard shows the error forever and never sees the recovery.
func TestSubErrMsgKeepsListening(t *testing.T) {
	m := dashboardModel()
	m.snapCh = make(chan []*daemon.JobView)
	m.errCh = make(chan error)

	next, cmd := m.Update(subErrMsg{errors.New("connection reset")})
	m = next.(statusModel)
	if m.err == nil {
		t.Error("the error was not recorded for display")
	}
	if cmd == nil {
		t.Fatal("no follow-up command: the model stopped waiting for snapshots")
	}

	// The next snapshot clears the error.
	next, _ = m.Update(jobsMsg(nil))
	if next.(statusModel).err != nil {
		t.Error("a fresh snapshot did not clear the connection error")
	}
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// TestCtrlCQuitsFromEveryScreen: the dashboard's own "q" isn't available
// inside an overlay, but Ctrl+C is the universal way out and must work
// there — including while a text field has focus.
func TestCtrlCQuitsFromEveryScreen(t *testing.T) {
	overlays := map[string]func(*statusModel){
		"dashboard": func(*statusModel) {},
		"newjob":    func(m *statusModel) { m.newJob = &newJobState{step: newJobEnterLink} },
		"webdav":    func(m *statusModel) { m.webdavBrowse = &webdavBrowseState{step: webdavPickConn} },
		"settings":  func(m *statusModel) { m.settings = &settingsState{editing: true} },
	}
	for name, open := range overlays {
		t.Run(name, func(t *testing.T) {
			m := dashboardModel()
			open(&m)
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
			if !isQuit(cmd) {
				t.Error("ctrl+c did not quit")
			}
		})
	}
}

// TestKeypressDismissesStatusMessage: a leftover message ("remove
// canceled", "playing …") used to outrank the failed row's error in the
// footer until some later action replaced it.
func TestKeypressDismissesStatusMessage(t *testing.T) {
	failed := jobView("job1")
	failed.Status = store.StatusFailed
	failed.ErrorMsg = "connection refused"
	m := dashboardModel(failed)
	m.statusMsg = "remove canceled"

	if !strings.Contains(m.View(), "remove canceled") {
		t.Fatal("precondition: the status message should be showing")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	view := next.(statusModel).View()
	if strings.Contains(view, "remove canceled") {
		t.Error("the stale message survived a keypress")
	}
	if !strings.Contains(view, "connection refused") {
		t.Error("the failed job's error is still hidden after the message was dismissed")
	}
}

// TestRemoveConfirmationSurvivesUntilAnswered: dismissing on keypress
// must not eat the pending y/N prompt's own text before it's answered.
func TestRemoveConfirmationSurvivesUntilAnswered(t *testing.T) {
	m := dashboardModel(jobView("job1"))
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = next.(statusModel)
	if m.confirmRemove == nil || !strings.Contains(m.View(), "[y/N]") {
		t.Fatalf("d did not arm a visible confirmation (statusMsg=%q)", m.statusMsg)
	}
}

// TestActionTargetsFollowDisplayOrder: selection is a map, so returning
// its iteration order made bulk actions (and their "first error")
// nondeterministic.
func TestActionTargetsFollowDisplayOrder(t *testing.T) {
	m := dashboardModel(jobView("job1"), jobView("job2"), jobView("job3"), jobView("job4"))
	m.selected = map[string]bool{"job4": true, "job1": true, "job3": true}
	want := []string{"job1", "job3", "job4"}
	for i := 0; i < 20; i++ {
		if got := m.actionTargets(); !reflect.DeepEqual(got, want) {
			t.Fatalf("actionTargets() = %v, want %v", got, want)
		}
	}
}
