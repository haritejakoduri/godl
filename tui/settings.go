package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"godl/internal/daemon"
	"godl/internal/format"
	"godl/internal/prefs"
	"godl/internal/store"
)

// settingsState is the TUI's Settings tab: every option in
// internal/prefs, grouped by section, edited in place and saved on each
// change — there's no separate save step to forget.
type settingsState struct {
	loading bool
	err     string
	saved   bool // true right after a successful save, until the next interaction

	// current is the daemon's last-known-good settings — what every
	// field displays, and the base a field's own edit is applied on top
	// of before being sent back to the daemon.
	current store.Settings

	cursor  int // index into prefs.Fields
	editing bool
	input   textinput.Model
}

var (
	settingsSectionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#5FD6C9"))
	settingsCursorStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0B0B0B")).Background(lipgloss.Color("#5FD6C9"))
	settingsDimStyle     = lipgloss.NewStyle().Faint(true)
)

// settingsLabelWidth fits the longest label with a little room.
const settingsLabelWidth = 28

// loadSettings fetches the daemon's current settings for the Settings
// tab to display — called once when the tab is opened.
func loadSettings(st *settingsState) tea.Cmd {
	return func() tea.Msg {
		if err := daemon.EnsureRunning(); err != nil {
			return settingsLoadedMsg{st: st, err: err}
		}
		resp, err := daemon.Call(daemon.Request{Cmd: daemon.CmdGetSettings})
		if err != nil {
			return settingsLoadedMsg{st: st, err: err}
		}
		if resp.Settings == nil {
			return settingsLoadedMsg{st: st, err: fmt.Errorf("daemon returned no settings")}
		}
		return settingsLoadedMsg{st: st, settings: *resp.Settings}
	}
}

// saveSettings sends s to the daemon to validate and persist — called
// immediately after every single-field edit.
func saveSettings(st *settingsState, s store.Settings) tea.Cmd {
	return func() tea.Msg {
		resp, err := daemon.Call(daemon.Request{Cmd: daemon.CmdSetSettings, Settings: &s})
		if err != nil {
			return settingsSavedMsg{st: st, err: err}
		}
		if resp.Settings == nil {
			return settingsSavedMsg{st: st, err: fmt.Errorf("daemon returned no settings")}
		}
		return settingsSavedMsg{st: st, settings: *resp.Settings}
	}
}

// commit applies change to a copy of the current settings and saves it,
// or reports why it was refused without touching anything.
func (s *settingsState) commit(change func(*store.Settings) error) tea.Cmd {
	working := s.current
	if err := change(&working); err != nil {
		s.err = err.Error()
		return nil
	}
	s.err, s.saved = "", false
	return saveSettings(s, working)
}

// updateSettings handles a keypress while the Settings tab is open.
func (m statusModel) updateSettings(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.settings
	if s.loading {
		if msg.String() == "esc" {
			m.settings = nil
		}
		return m, nil
	}
	field := prefs.Fields[s.cursor]

	if s.editing {
		switch msg.String() {
		case "esc":
			s.editing = false
			s.err = ""
			return m, nil
		case "enter":
			cmd := s.commit(func(set *store.Settings) error { return field.Set(set, s.input.Value()) })
			if cmd != nil {
				s.editing = false
			}
			return m, cmd
		default:
			var cmd tea.Cmd
			s.input, cmd = s.input.Update(msg)
			return m, cmd
		}
	}

	switch msg.String() {
	case "esc", "q":
		m.settings = nil
		return m, nil
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
			s.err, s.saved = "", false
		}
		return m, nil
	case "down", "j":
		if s.cursor < len(prefs.Fields)-1 {
			s.cursor++
			s.err, s.saved = "", false
		}
		return m, nil
	case "home", "g":
		s.cursor, s.err, s.saved = 0, "", false
		return m, nil
	case "end", "G":
		s.cursor, s.err, s.saved = len(prefs.Fields)-1, "", false
		return m, nil
	case "left", "h", "right", "l":
		if field.Kind != prefs.Choice && field.Kind != prefs.Toggle {
			return m, nil
		}
		dir := 1
		if k := msg.String(); k == "left" || k == "h" {
			dir = -1
		}
		return m, s.commit(func(set *store.Settings) error { return field.Cycle(set, dir) })
	case "r":
		return m, s.commit(func(set *store.Settings) error { field.Reset(set); return nil })
	case "enter", " ":
		if field.Kind == prefs.Choice || field.Kind == prefs.Toggle {
			return m, s.commit(func(set *store.Settings) error { return field.Cycle(set, 1) })
		}
		ti := textinput.New()
		ti.SetValue(field.Get(s.current))
		ti.CursorEnd()
		ti.Focus()
		ti.CharLimit = 512
		ti.Width = max(24, min(60, m.width-settingsLabelWidth-8))
		ti.Placeholder = field.Unset
		s.input = ti
		s.editing = true
		s.err = ""
		return m, nil
	}
	return m, nil
}

// settingsValue is how a field's value reads in the list, fitted to
// width cells (0 = unlimited), with hints for the selected row: arrows
// around a choice, and a note when an environment variable is
// overriding the saved value.
func settingsValue(f prefs.Field, s store.Settings, selected bool, width int) string {
	v := format.ShortenHome(f.Display(s))
	if selected && (f.Kind == prefs.Choice || f.Kind == prefs.Toggle) {
		v = "‹ " + v + " ›"
	}
	note := ""
	if f.Overridden() {
		note = "  (overridden by " + f.EnvOverride + ")"
	}
	if width > 0 {
		v = keepTail(v, max(width-lipgloss.Width(note), 8))
	}
	return v + settingsDimStyle.Render(note)
}

// keepTail shortens s to w cells by cutting from the front, since the
// end of a path is the part that tells folders apart.
func keepTail(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > w {
		r = r[1:]
	}
	return "…" + string(r)
}

func (m statusModel) viewSettings() string {
	s := m.settings
	var b strings.Builder
	b.WriteString(m.wrapped(titleStyle).Render(brandStyle.Render("godl") + "  Settings" + settingsDimStyle.Render("  ·  changes save instantly  ·  also: godl settings")))
	b.WriteString("\n")

	if s.loading {
		b.WriteString("\n  loading...\n")
		b.WriteString(m.helpView("esc close"))
		return b.String()
	}

	// The list, one line per section header and per field, then a
	// window of it that keeps the cursor in view on a short terminal.
	var lines []string
	cursorLine := 0
	section := ""
	for i, f := range prefs.Fields {
		if f.Section != section {
			if section != "" {
				lines = append(lines, "")
			}
			section = f.Section
			lines = append(lines, " "+settingsSectionStyle.Render(section))
		}
		label := fitCells(f.Label, settingsLabelWidth)
		valueW := 0
		if m.width > 0 {
			valueW = m.width - settingsLabelWidth - 6
		}
		value := settingsValue(f, s.current, i == s.cursor, valueW)
		if i == s.cursor {
			cursorLine = len(lines)
			if s.editing {
				value = s.input.View()
			}
			lines = append(lines, " "+settingsCursorStyle.Render(" "+label+" ")+"  "+value)
			continue
		}
		lines = append(lines, "   "+label+"  "+value)
	}

	field := prefs.Fields[s.cursor]
	var detail []string
	detail = append(detail, m.wrapped(lipgloss.NewStyle().Padding(0, 1)).Render(field.Help))
	cli := fmt.Sprintf("godl settings set %s <value>", field.Key)
	if field.Kind == prefs.Choice {
		values := make([]string, len(field.Options))
		for i, o := range field.Options {
			values[i] = o.Value
		}
		cli = fmt.Sprintf("godl settings set %s %s", field.Key, strings.Join(values, "|"))
	}
	detail = append(detail, m.wrapped(settingsDimStyle.Padding(0, 1)).Render("CLI: "+cli))
	switch {
	case s.err != "":
		detail = append(detail, m.wrapped(errStyle).Render("error: "+s.err))
	case s.saved:
		detail = append(detail, m.wrapped(statStyle).Render("saved"))
	}
	var help string
	switch {
	case s.editing:
		help = m.helpView("enter save  esc cancel  (leave empty for the default)")
	case field.Kind == prefs.Choice || field.Kind == prefs.Toggle:
		help = m.helpView("↑/↓ select  ←/→ or enter change  r reset to default  esc close")
	default:
		help = m.helpView("↑/↓ select  enter edit  r reset to default  esc close")
	}
	footer := "\n\n" + strings.Join(detail, "\n") + "\n" + help

	avail := len(lines)
	if m.height > 0 {
		avail = m.height - lipgloss.Height(b.String()) - lipgloss.Height(footer) - 1
	}
	if avail < 3 {
		avail = 3
	}
	start := 0
	if len(lines) > avail {
		start = min(max(cursorLine-avail/2, 0), len(lines)-avail)
		lines = lines[start : start+avail]
	}
	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString(footer)
	return b.String()
}

func boolLabel(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
