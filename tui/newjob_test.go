package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"godl/internal/daemon"
)

func newJobModel() statusModel {
	ti := textinput.New()
	ti.Focus()
	return statusModel{newJob: &newJobState{step: newJobPickType, input: ti}}
}

func press(m statusModel, keys ...string) statusModel {
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		next, _ := m.updateNewJob(msg)
		m = next.(statusModel)
	}
	return m
}

func typeIndexOf(t *testing.T, cmd string) int {
	t.Helper()
	for i, nt := range newJobTypes {
		if nt.cmd == cmd {
			return i
		}
	}
	t.Fatalf("no new-job type for %s", cmd)
	return -1
}

// TestNewJobWizardSkipsPresetsForNonSocial: only yt-dlp jobs have quality
// presets; URL and torrent go straight to the link prompt, and esc
// returns to where the user came from.
func TestNewJobWizardSkipsPresetsForNonSocial(t *testing.T) {
	m := press(newJobModel(), "enter") // first type: URL
	if m.newJob.step != newJobEnterLink {
		t.Fatalf("URL should go straight to the link prompt, got step %d", m.newJob.step)
	}
	m = press(m, "esc")
	if m.newJob.step != newJobPickType {
		t.Errorf("esc from the link prompt of a URL job should return to the type list, got %d", m.newJob.step)
	}
}

func TestNewJobWizardSocialPicksAPresetFirst(t *testing.T) {
	m := newJobModel()
	m.newJob.typeIndex = typeIndexOf(t, daemon.CmdAddSocial)
	m = press(m, "enter")
	if m.newJob.step != newJobPickPreset {
		t.Fatalf("social should pick a preset first, got step %d", m.newJob.step)
	}
	m = press(m, "down", "enter")
	if m.newJob.step != newJobEnterLink || m.newJob.presetIndex != 1 {
		t.Fatalf("step=%d preset=%d, want the link prompt with preset 1", m.newJob.step, m.newJob.presetIndex)
	}
	m = press(m, "esc")
	if m.newJob.step != newJobPickPreset {
		t.Errorf("esc from a social link prompt should return to the presets, got %d", m.newJob.step)
	}
	m = press(m, "esc", "esc")
	if m.newJob != nil {
		t.Error("esc from the first screen should close the wizard")
	}
}

func TestNewJobWizardStartsTheJob(t *testing.T) {
	m := press(newJobModel(), "enter")

	// An empty link does nothing rather than starting a job for "".
	next, cmd := m.updateNewJob(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || next.(statusModel).newJob == nil {
		t.Fatal("enter on an empty link should be ignored")
	}

	m = press(m, "https://example.com/a.iso")
	next, cmd = m.updateNewJob(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(statusModel)
	if cmd == nil {
		t.Fatal("enter on a link should return the start command")
	}
	if m.newJob != nil {
		t.Error("the wizard should close once the job is being started")
	}
	if m.statusMsg != "starting..." {
		t.Errorf("statusMsg = %q, want the in-progress note", m.statusMsg)
	}
}
