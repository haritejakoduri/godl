package tui

import (
	"context"
	"errors"
	"testing"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/table"
	"github.com/muesli/termenv"

	"godl/internal/daemon"
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
