package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"godl/internal/daemon"
	"godl/internal/ratelimit"
	"godl/internal/store"
)

// settingsState is the TUI's Settings tab: a small fixed list of the
// daemon's configurable defaults (see store.Settings), edited in place
// and saved immediately on each change rather than needing a separate
// "save" step — same immediate-apply model as toggling a checkbox in
// most settings screens, so there's nothing to lose by navigating away
// or quitting mid-edit.
type settingsState struct {
	loading bool
	err     string
	saved   bool // true right after a successful save, until the next interaction

	// current is the daemon's last-known-good settings — what every
	// field displays, and the base a field's own edit is applied on top
	// of before being sent back to the daemon.
	current store.Settings

	cursor  int // index into settingsFields
	editing bool
	input   textinput.Model
}

type settingsFieldKind int

const (
	settingsFieldInt settingsFieldKind = iota
	settingsFieldText
	settingsFieldBool
)

// settingsField describes one row of the Settings tab: how to display
// current's value for it, and how to apply an edit back onto a
// store.Settings copy. get/set are used for int/text fields (set
// parses+validates the raw textinput value); toggle is used for bool
// fields instead — exactly one of the two is ever called for a given
// field, per its kind.
type settingsField struct {
	label  string
	help   string
	kind   settingsFieldKind
	get    func(store.Settings) string
	set    func(*store.Settings, string) error
	toggle func(*store.Settings)
}

func boolLabel(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

var settingsFields = []settingsField{
	{
		label: "Max concurrent downloads",
		help:  "0 = unlimited. Extra jobs queue and start as running ones finish.",
		kind:  settingsFieldInt,
		get:   func(s store.Settings) string { return strconv.Itoa(s.MaxConcurrent) },
		set: func(s *store.Settings, v string) error {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 0 {
				return fmt.Errorf("must be a whole number, 0 or more")
			}
			s.MaxConcurrent = n
			return nil
		},
	},
	{
		label: "Default rate limit",
		help:  `e.g. "2M" or "500K"; empty = unlimited. Applied to a job that doesn't pass its own -R.`,
		kind:  settingsFieldText,
		get:   func(s store.Settings) string { return s.DefaultRateLimit },
		set: func(s *store.Settings, v string) error {
			v = strings.TrimSpace(v)
			if v != "" {
				if _, err := ratelimit.ParseRate(v); err != nil {
					return err
				}
			}
			s.DefaultRateLimit = v
			return nil
		},
	},
	{
		label: "Global bandwidth limit",
		help:  `e.g. "5M"; empty = unlimited. Caps every job's transfer COMBINED, not each one separately (a true shared cap for url/webdav; torrent and social are each individually capped at it instead — see README).`,
		kind:  settingsFieldText,
		get:   func(s store.Settings) string { return s.GlobalRateLimit },
		set: func(s *store.Settings, v string) error {
			v = strings.TrimSpace(v)
			if v != "" {
				if _, err := ratelimit.ParseRate(v); err != nil {
					return err
				}
			}
			s.GlobalRateLimit = v
			return nil
		},
	},
	{
		label:  "Auto-retry on failure",
		help:   "Automatically re-queues a failed job after a backoff delay instead of leaving it failed.",
		kind:   settingsFieldBool,
		get:    func(s store.Settings) string { return boolLabel(s.AutoRetry) },
		toggle: func(s *store.Settings) { s.AutoRetry = !s.AutoRetry },
	},
	{
		label: "Auto-retry max attempts",
		help:  "How many times to auto-retry before leaving a job failed for good.",
		kind:  settingsFieldInt,
		get:   func(s store.Settings) string { return strconv.Itoa(s.AutoRetryMaxAttempts) },
		set: func(s *store.Settings, v string) error {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil || n < 1 {
				return fmt.Errorf("must be a whole number, at least 1")
			}
			s.AutoRetryMaxAttempts = n
			return nil
		},
	},
	{
		label:  "Notify on completion",
		help:   "Fires a desktop notification when a job finishes successfully (best-effort; needs notify-send on Linux or macOS).",
		kind:   settingsFieldBool,
		get:    func(s store.Settings) string { return boolLabel(s.NotifyOnComplete) },
		toggle: func(s *store.Settings) { s.NotifyOnComplete = !s.NotifyOnComplete },
	},
}

// loadSettings fetches the daemon's current settings for the Settings
// tab to display — called once when the tab is opened.
func loadSettings() tea.Cmd {
	return func() tea.Msg {
		if err := daemon.EnsureRunning(); err != nil {
			return settingsLoadedMsg{err: err}
		}
		resp, err := daemon.Call(daemon.Request{Cmd: daemon.CmdGetSettings})
		if err != nil {
			return settingsLoadedMsg{err: err}
		}
		if resp.Settings == nil {
			return settingsLoadedMsg{err: fmt.Errorf("daemon returned no settings")}
		}
		return settingsLoadedMsg{settings: *resp.Settings}
	}
}

// saveSettings sends s to the daemon to validate and persist — called
// immediately after every single-field edit (see settingsState's own
// doc comment for why there's no separate save step).
func saveSettings(s store.Settings) tea.Cmd {
	return func() tea.Msg {
		resp, err := daemon.Call(daemon.Request{Cmd: daemon.CmdSetSettings, Settings: &s})
		if err != nil {
			return settingsSavedMsg{err: err}
		}
		if resp.Settings == nil {
			return settingsSavedMsg{err: fmt.Errorf("daemon returned no settings")}
		}
		return settingsSavedMsg{settings: *resp.Settings}
	}
}

// updateSettings handles a keypress while the Settings tab is open —
// called from statusModel.Update, mirroring updateNewJob/
// updateWebDAVBrowse's own per-overlay update methods.
func (m statusModel) updateSettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.settings

	if s.editing {
		switch msg.String() {
		case "esc":
			s.editing = false
			s.err = ""
			return m, nil
		case "enter":
			field := settingsFields[s.cursor]
			working := s.current
			if err := field.set(&working, s.input.Value()); err != nil {
				s.err = err.Error()
				return m, nil
			}
			s.editing = false
			s.err = ""
			s.saved = false
			return m, saveSettings(working)
		default:
			var cmd tea.Cmd
			s.input, cmd = s.input.Update(msg)
			return m, cmd
		}
	}

	switch msg.String() {
	case "esc":
		m.settings = nil
		return m, nil
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
			s.err = ""
		}
		return m, nil
	case "down", "j":
		if s.cursor < len(settingsFields)-1 {
			s.cursor++
			s.err = ""
		}
		return m, nil
	case "enter", " ":
		field := settingsFields[s.cursor]
		if field.kind == settingsFieldBool {
			working := s.current
			field.toggle(&working)
			s.err = ""
			s.saved = false
			return m, saveSettings(working)
		}
		ti := textinput.New()
		ti.SetValue(field.get(s.current))
		ti.Focus()
		ti.CharLimit = 32
		ti.Width = 24
		s.input = ti
		s.editing = true
		s.err = ""
		return m, nil
	}
	return m, nil
}

func (m statusModel) viewSettings() string {
	s := m.settings
	var b strings.Builder
	b.WriteString(statStyle.Render("Settings"))
	b.WriteString("\n")

	if s.loading {
		b.WriteString("loading...\n")
		b.WriteString(helpStyle.Render("esc close"))
		return b.String()
	}

	for i, f := range settingsFields {
		cursor := "  "
		if i == s.cursor {
			cursor = "> "
		}
		value := f.get(s.current)
		if i == s.cursor && s.editing {
			value = s.input.View()
		}
		fmt.Fprintf(&b, "%s%-28s %s\n", cursor, f.label, value)
		if i == s.cursor {
			b.WriteString("    " + helpStyle.Render(f.help) + "\n")
		}
	}

	switch {
	case s.err != "":
		b.WriteString(errStyle.Render("error: " + s.err))
		b.WriteString("\n")
	case s.saved:
		b.WriteString(statStyle.Render("saved"))
		b.WriteString("\n")
	}

	if s.editing {
		b.WriteString(helpStyle.Render("enter save  esc cancel"))
	} else {
		b.WriteString(helpStyle.Render("↑/↓ select  enter edit/toggle  esc close"))
	}
	return b.String()
}
