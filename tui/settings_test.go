package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"godl/internal/prefs"
	"godl/internal/store"
)

func fieldIndex(t *testing.T, key string) int {
	t.Helper()
	for i, f := range prefs.Fields {
		if f.Key == key {
			return i
		}
	}
	t.Fatalf("no settings field %q", key)
	return -1
}

func settingsModel(t *testing.T, s store.Settings, key string) statusModel {
	return statusModel{settings: &settingsState{current: s, cursor: fieldIndex(t, key)}}
}

func TestUpdateSettingsNavigatesFieldsWithinBounds(t *testing.T) {
	m := statusModel{settings: &settingsState{current: store.DefaultSettings()}}

	mm, _ := m.updateSettings(key("down"))
	m = mm.(statusModel)
	if m.settings.cursor != 1 {
		t.Fatalf("cursor after one down = %d, want 1", m.settings.cursor)
	}

	mm, _ = m.updateSettings(key("up"))
	m = mm.(statusModel)
	if m.settings.cursor != 0 {
		t.Fatalf("cursor after down then up = %d, want 0", m.settings.cursor)
	}

	// Up at the top stays put rather than going negative.
	mm, _ = m.updateSettings(key("up"))
	m = mm.(statusModel)
	if m.settings.cursor != 0 {
		t.Fatalf("cursor after up at the top = %d, want 0 (clamped)", m.settings.cursor)
	}

	// Down past the last field stays put rather than going out of range.
	for i := 0; i < len(prefs.Fields)+2; i++ {
		mm, _ = m.updateSettings(key("down"))
		m = mm.(statusModel)
	}
	if want := len(prefs.Fields) - 1; m.settings.cursor != want {
		t.Fatalf("cursor after overshooting down = %d, want %d (clamped)", m.settings.cursor, want)
	}
}

func TestUpdateSettingsEscClosesOverlayWhenNotEditing(t *testing.T) {
	m := statusModel{settings: &settingsState{current: store.DefaultSettings()}}
	mm, _ := m.updateSettings(key("esc"))
	m = mm.(statusModel)
	if m.settings != nil {
		t.Fatal("esc while not editing should close the Settings tab (m.settings = nil)")
	}
}

// Enter on a number field opens editing pre-filled with the current
// value; typing replaces it, and enter commits — producing a save
// command and leaving edit mode.
func TestUpdateSettingsTextFieldEditRoundTrip(t *testing.T) {
	m := settingsModel(t, store.Settings{MaxConcurrent: 4, AutoRetryMaxAttempts: 3}, "max_concurrent")

	mm, cmd := m.updateSettings(key("enter"))
	m = mm.(statusModel)
	if !m.settings.editing {
		t.Fatal("enter on a text/int field should enter edit mode")
	}
	if cmd != nil {
		t.Fatal("entering edit mode should not itself dispatch a save")
	}
	if got := m.settings.input.Value(); got != "4" {
		t.Fatalf("edit input pre-filled with %q, want the current value %q", got, "4")
	}

	for range m.settings.input.Value() {
		mm, _ = m.updateSettings(key("backspace"))
		m = mm.(statusModel)
	}
	mm, _ = m.updateSettings(key("7"))
	m = mm.(statusModel)

	mm, cmd = m.updateSettings(key("enter"))
	m = mm.(statusModel)
	if m.settings.editing {
		t.Fatal("committing a valid edit should leave edit mode")
	}
	if cmd == nil {
		t.Fatal("committing a valid edit should dispatch a save command")
	}
	if m.settings.err != "" {
		t.Fatalf("committing a valid edit set an error: %q", m.settings.err)
	}
}

// An unparseable value must not be sent to the daemon: it surfaces an
// error and stays in edit mode so the user can fix it.
func TestUpdateSettingsRejectsInvalidEditValue(t *testing.T) {
	m := settingsModel(t, store.DefaultSettings(), "max_concurrent")

	mm, _ := m.updateSettings(key("enter"))
	m = mm.(statusModel)
	for range m.settings.input.Value() {
		mm, _ = m.updateSettings(key("backspace"))
		m = mm.(statusModel)
	}
	mm, _ = m.updateSettings(key("x"))
	m = mm.(statusModel)

	mm, cmd := m.updateSettings(key("enter"))
	m = mm.(statusModel)
	if cmd != nil {
		t.Fatal("an invalid value must not dispatch a save")
	}
	if !m.settings.editing {
		t.Fatal("an invalid value should stay in edit mode, not silently close it")
	}
	if m.settings.err == "" {
		t.Fatal("an invalid value should set settings.err")
	}
}

// Toggles and choices change and save on a single key, never opening
// the text editor.
func TestUpdateSettingsTogglesAndChoicesSaveImmediately(t *testing.T) {
	for _, k := range []string{"auto_retry", "social_preset", "cookies_from_browser", "animations"} {
		m := settingsModel(t, store.DefaultSettings(), k)
		mm, cmd := m.updateSettings(key("enter"))
		m = mm.(statusModel)
		if m.settings.editing {
			t.Errorf("%s: enter opened the text editor", k)
		}
		if cmd == nil {
			t.Errorf("%s: enter didn't dispatch a save", k)
		}
	}
}

func TestChoiceFieldsCycleBothWays(t *testing.T) {
	f := prefs.Fields[fieldIndex(t, "social_preset")]
	s := store.DefaultSettings()
	if err := f.Cycle(&s, 1); err != nil {
		t.Fatal(err)
	}
	if s.SocialPreset != "1080p" {
		t.Fatalf("next after best = %q, want 1080p", s.SocialPreset)
	}
	f.Cycle(&s, -1)
	f.Cycle(&s, -1)
	if s.SocialPreset != "audio" {
		t.Errorf("back past the first option = %q, want it to wrap to the last (audio)", s.SocialPreset)
	}
}

func TestResetKeySavesTheDefault(t *testing.T) {
	m := settingsModel(t, store.Settings{Connections: 16, AutoRetryMaxAttempts: 3}, "connections")
	mm, cmd := m.updateSettings(key("r"))
	m = mm.(statusModel)
	if cmd == nil || m.settings.err != "" {
		t.Fatalf("reset didn't save (err %q)", m.settings.err)
	}
}

// On a terminal too short for every setting, the list scrolls so the
// selected one is always on screen, and the view still fits.
func TestSettingsViewKeepsTheCursorVisibleOnShortTerminals(t *testing.T) {
	for _, k := range []string{"download_dir", "seed_ratio", "animations"} {
		m := settingsModel(t, store.DefaultSettings(), k)
		m.width, m.height = 80, 14
		view := m.viewSettings()
		if h := lipgloss.Height(view); h > m.height {
			t.Errorf("%s: view is %d lines on a %d-line terminal", k, h, m.height)
		}
		if label := prefs.Fields[fieldIndex(t, k)].Label; !strings.Contains(view, label) {
			t.Errorf("%s: the selected field %q scrolled out of view:\n%s", k, label, view)
		}
	}
}

func TestSettingsViewShowsEverySectionOnATallTerminal(t *testing.T) {
	m := settingsModel(t, store.DefaultSettings(), "download_dir")
	m.width, m.height = 120, 60
	view := m.viewSettings()
	for _, sec := range prefs.Sections {
		if !strings.Contains(view, sec) {
			t.Errorf("section %q missing from the Settings tab", sec)
		}
	}
}

func TestSettingsKeyOpensOverlay(t *testing.T) {
	m := statusModel{}
	mm, cmd := m.Update(key("s"))
	got := mm.(statusModel)
	if got.settings == nil {
		t.Fatal(`"s" should open the Settings tab (m.settings != nil)`)
	}
	if !got.settings.loading {
		t.Fatal("a freshly opened Settings tab should start in loading state")
	}
	if cmd == nil {
		t.Fatal(`"s" should dispatch loadSettings()`)
	}
}

func TestSettingsLoadedMsgIgnoredAfterOverlayClosed(t *testing.T) {
	m := statusModel{}
	mm, _ := m.Update(settingsLoadedMsg{settings: store.DefaultSettings()})
	got := mm.(statusModel)
	if got.settings != nil {
		t.Fatal("a late settingsLoadedMsg should not reopen the Settings tab")
	}
}

// A save updates the settings the dashboard itself uses, so turning
// animations off takes effect without restarting.
func TestSavedSettingsApplyToTheDashboard(t *testing.T) {
	st := &settingsState{current: store.DefaultSettings()}
	m := statusModel{settings: st, anim: testAnim()}
	mm, _ := m.Update(settingsSavedMsg{st: st, settings: store.Settings{AutoRetryMaxAttempts: 3, NoAnimations: true}})
	got := mm.(statusModel)
	if !got.prefs.NoAnimations {
		t.Error("the saved settings didn't reach the dashboard")
	}
	if !got.anim.off {
		t.Error("switching animations off in Settings didn't stop them")
	}
}

func TestWizardStartsOnTheDefaultQuality(t *testing.T) {
	m := dashboardModel()
	m.prefs.SocialPreset = "720p"
	mm, _ := m.Update(key("n"))
	got := mm.(statusModel)
	if got.newJob == nil || got.newJob.presetIndex != presetIndex("720p") || presetIndex("720p") == 0 {
		t.Errorf("wizard preset cursor = %+v, want it on 720p", got.newJob)
	}
}

func TestLongSettingValuesKeepTheirEndAndFit(t *testing.T) {
	s := store.DefaultSettings()
	s.DownloadDir = "/very/long/path/that/goes/on/and/on/and/on/until/it/reaches/Films"
	m := settingsModel(t, s, "download_dir")
	m.width, m.height = 70, 40
	view := m.viewSettings()
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > m.width && strings.Contains(line, "Films") {
			t.Errorf("value line is %d cells on a %d-wide terminal: %q", w, m.width, line)
		}
	}
	if !strings.Contains(view, "…") || !strings.Contains(view, "reaches/Films") {
		t.Errorf("long path wasn't shortened from the front:\n%s", view)
	}
}
