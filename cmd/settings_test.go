package cmd

import (
	"testing"

	"godl/internal/store"
)

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
	for i := 0; i < len(settingsFields)+2; i++ {
		mm, _ = m.updateSettings(key("down"))
		m = mm.(statusModel)
	}
	if want := len(settingsFields) - 1; m.settings.cursor != want {
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

// TestUpdateSettingsTextFieldEditRoundTrip drives the "Max concurrent
// downloads" field (index 0, an int field): enter opens editing
// pre-filled with the current value, typing replaces it, and enter
// commits — which must produce a save tea.Cmd (proving it validated
// successfully and is about to call the daemon), not leave the overlay
// still in edit mode.
func TestUpdateSettingsTextFieldEditRoundTrip(t *testing.T) {
	m := statusModel{settings: &settingsState{current: store.Settings{MaxConcurrent: 4, AutoRetryMaxAttempts: 3}}}

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

	// Replace the prefilled value entirely.
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

// TestUpdateSettingsRejectsInvalidEditValue is the failure-path
// counterpart: an unparseable value must not be silently accepted or
// sent to the daemon — it should surface an error and stay in edit mode
// so the user can fix it.
func TestUpdateSettingsRejectsInvalidEditValue(t *testing.T) {
	m := statusModel{settings: &settingsState{current: store.DefaultSettings()}}

	mm, _ := m.updateSettings(key("enter")) // open edit on MaxConcurrent
	m = mm.(statusModel)
	for range m.settings.input.Value() {
		mm, _ = m.updateSettings(key("backspace"))
		m = mm.(statusModel)
	}
	mm, _ = m.updateSettings(key("x")) // not a number
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

// TestUpdateSettingsTogglesBoolFieldImmediately confirms a bool field
// (Auto-retry on failure, index 2) toggles and dispatches a save on a
// single enter — it never enters the text-edit sub-mode a numeric/text
// field does.
func TestUpdateSettingsTogglesBoolFieldImmediately(t *testing.T) {
	autoRetryFieldIdx := -1
	for i, f := range settingsFields {
		if f.kind == settingsFieldBool {
			autoRetryFieldIdx = i
			break
		}
	}
	if autoRetryFieldIdx < 0 {
		t.Fatal("no bool field found in settingsFields")
	}

	m := statusModel{settings: &settingsState{current: store.DefaultSettings(), cursor: autoRetryFieldIdx}}
	mm, cmd := m.updateSettings(key("enter"))
	m = mm.(statusModel)
	if m.settings.editing {
		t.Fatal("a bool field's enter should not enter text-edit mode")
	}
	if cmd == nil {
		t.Fatal("toggling a bool field should dispatch a save command")
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
	// A settingsLoadedMsg arriving after the user already closed the
	// tab (m.settings == nil) must not panic or resurrect the overlay.
	m := statusModel{}
	mm, _ := m.Update(settingsLoadedMsg{settings: store.DefaultSettings()})
	got := mm.(statusModel)
	if got.settings != nil {
		t.Fatal("a late settingsLoadedMsg should not reopen the Settings tab")
	}
}
