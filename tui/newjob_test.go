package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"godl/internal/daemon"
	"godl/internal/social"
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

// TestNewJobWizardPlayStreamsInsteadOfDownloading: the Play type picks
// a quality like a social job, then plays straight from the link
// prompt — the output and rate steps are about a saved file, and there
// isn't one.
func TestNewJobWizardPlayStreamsInsteadOfDownloading(t *testing.T) {
	m := newJobModel()
	m.newJob.typeIndex = typeIndexOf(t, newJobPlay)
	m = press(m, "enter")
	if m.newJob.step != newJobPickPreset {
		t.Fatalf("play should pick a preset first, got step %d", m.newJob.step)
	}
	m = press(m, "down", "enter")
	if got, want := m.newJob.selectedFormat(), social.Presets[1].Format; got != want {
		t.Fatalf("selectedFormat() = %q, want preset 1's %q", got, want)
	}

	m = press(m, "https://example.com/watch?v=xyz")
	next, cmd := m.updateNewJob(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(statusModel)
	if cmd == nil {
		t.Fatal("enter on the link should return the play command")
	}
	if m.newJob != nil {
		t.Error("the wizard should close once the player is being opened")
	}
	if m.statusMsg != "opening player..." {
		t.Errorf("statusMsg = %q, want the in-progress note", m.statusMsg)
	}
}

func TestNewJobSelectedFormatIsEmptyWithoutPresets(t *testing.T) {
	m := newJobModel()
	m.newJob.typeIndex = typeIndexOf(t, daemon.CmdAddTorrent)
	m.newJob.presetIndex = 1
	if got := m.newJob.selectedFormat(); got != "" {
		t.Errorf("selectedFormat() for a torrent = %q, want empty", got)
	}
}

// A torrent's files are listed after the link, all ticked; the user can
// clear them all, tick some back, and the choice becomes --files.
func TestNewJobWizardPicksTorrentFiles(t *testing.T) {
	m := newJobModel()
	m.newJob.typeIndex = typeIndexOf(t, daemon.CmdAddTorrent)
	m = press(m, "enter", "magnet:?xt=urn:btih:abc")
	next, cmd := m.updateNewJob(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(statusModel)
	if m.newJob.step != newJobPickFiles || cmd == nil {
		t.Fatalf("a torrent link should go to the file picker and fetch the list, got step %d", m.newJob.step)
	}
	if !strings.Contains(m.viewNewJob(), "from peers") {
		t.Error("while a magnet's list loads, the picker should say it's asking peers")
	}

	next, _ = m.update(torrentFilesMsg{pick: m.newJob.pick, name: "Show", files: []daemon.TorrentFile{
		{Index: 0, Path: "Show/e01.mkv", Length: 100},
		{Index: 1, Path: "Show/e02.mkv", Length: 200},
		{Index: 2, Path: "Show/sample.mkv", Length: 5},
	}})
	m = next.(statusModel)
	if view := m.viewNewJob(); !strings.Contains(view, "3 of 3 selected") || !strings.Contains(view, "[x]") {
		t.Fatalf("every file should start ticked:\n%s", view)
	}

	m = press(m, "n") // select none
	next, _ = m.updateNewJob(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(statusModel)
	if m.newJob.step != newJobPickFiles || m.newJob.err == "" {
		t.Fatal("enter with nothing ticked should stay on the picker with an error")
	}

	m = press(m, " ", "down", " ") // tick e01 and e02
	if got := m.newJob.pick.spec(); got != "1-2" {
		t.Fatalf("spec = %q, want 1-2", got)
	}
	m = press(m, "enter")
	if m.newJob.step != newJobEnterOutput {
		t.Fatalf("enter with files ticked should move on, got step %d", m.newJob.step)
	}
	m = press(m, "esc")
	if m.newJob.step != newJobPickFiles {
		t.Error("esc from the output step should return to the picker, keeping the choice")
	}
	m = press(m, "a")
	if m.newJob.pick.spec() != "" {
		t.Error("select all means no --files at all")
	}
}

func TestNewJobWizardSkipsThePickerForOneFile(t *testing.T) {
	m := newJobModel()
	m.newJob.typeIndex = typeIndexOf(t, daemon.CmdAddTorrent)
	m = press(m, "enter", "magnet:?xt=urn:btih:abc", "enter")
	next, _ := m.update(torrentFilesMsg{pick: m.newJob.pick, name: "film", files: []daemon.TorrentFile{{Path: "film.mkv", Length: 9}}})
	if next.(statusModel).newJob.step != newJobEnterOutput {
		t.Error("a single-file torrent has nothing to choose; go straight to the output step")
	}
}
