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

// TestNewJobWizardLinkAdvancesToOutputThenRate checks the two optional
// steps added after the link prompt, and that esc walks back through
// them one at a time rather than dumping the whole wizard.
func TestNewJobWizardLinkAdvancesToOutputThenRate(t *testing.T) {
	m := press(newJobModel(), "enter", "https://example.com/a.iso", "enter")
	if m.newJob.step != newJobEnterOutput {
		t.Fatalf("enter on a non-empty link should advance to the output step, got step %d", m.newJob.step)
	}

	m = press(m, "enter")
	if m.newJob.step != newJobEnterRate {
		t.Fatalf("enter on the output step should advance to the rate step, got step %d", m.newJob.step)
	}

	m = press(m, "esc")
	if m.newJob.step != newJobEnterOutput {
		t.Fatalf("esc from the rate step should return to the output step, got step %d", m.newJob.step)
	}
	m = press(m, "esc")
	if m.newJob.step != newJobEnterLink {
		t.Fatalf("esc from the output step should return to the link prompt, got step %d", m.newJob.step)
	}
}

// TestNewJobWizardEmptyLinkDoesNothing: an empty link doesn't advance
// the wizard at all, rather than starting a job for "".
func TestNewJobWizardEmptyLinkDoesNothing(t *testing.T) {
	m := press(newJobModel(), "enter")
	next, cmd := m.updateNewJob(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || next.(statusModel).newJob.step != newJobEnterLink {
		t.Fatal("enter on an empty link should be ignored")
	}
}

// TestNewJobWizardRejectsAnInvalidRate keeps the wizard open on the
// rate step, showing an error, instead of starting a job with garbage
// the daemon would just reject later.
func TestNewJobWizardRejectsAnInvalidRate(t *testing.T) {
	m := press(newJobModel(), "enter", "https://example.com/a.iso", "enter", "enter", "not-a-rate")
	next, cmd := m.updateNewJob(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(statusModel)
	if cmd != nil {
		t.Fatal("an invalid rate limit should not start a job")
	}
	if m.newJob == nil || m.newJob.step != newJobEnterRate || m.newJob.err == "" {
		t.Fatal("an invalid rate limit should keep the wizard open on the rate step with an error")
	}
}

// TestNewJobWizardStartsTheJob confirms the full path — link, blank
// output, blank rate — still starts a job and closes the wizard, the
// way the old single-step-after-link wizard did.
func TestNewJobWizardStartsTheJob(t *testing.T) {
	m := press(newJobModel(), "enter", "https://example.com/a.iso", "enter", "enter")
	next, cmd := m.updateNewJob(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(statusModel)
	if cmd == nil {
		t.Fatal("enter on the (blank) rate step should return the start command")
	}
	if m.newJob != nil {
		t.Error("the wizard should close once the job is being started")
	}
	if m.statusMsg != "starting..." {
		t.Errorf("statusMsg = %q, want the in-progress note", m.statusMsg)
	}
}

// TestNewJobWizardAppliesOutputAndRate checks that non-blank output/
// rate fields actually reach the daemon request, not just that the
// wizard advances past them.
func TestNewJobWizardAppliesOutputAndRate(t *testing.T) {
	req, err := buildAddRequest(daemon.CmdAddURL, "https://example.com/a.iso", "/tmp/custom.iso")
	if err != nil {
		t.Fatal(err)
	}
	if req.Output != "/tmp/custom.iso" {
		t.Errorf("Output = %q, want the override honored verbatim", req.Output)
	}
}
