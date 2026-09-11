package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"godl/internal/daemon"
	"godl/internal/social"
)

type newJobStep int

const (
	newJobPickType   newJobStep = iota
	newJobPickPreset            // social only — skipped for url/torrent
	newJobEnterLink
)

type newJobState struct {
	step        newJobStep
	typeIndex   int
	presetIndex int // into social.Presets, social jobs only
	input       textinput.Model
}

// newJobTypes are the job types the TUI can start directly, in the
// order they're offered — mirroring the CLI's url/social/torrent
// subcommands.
var newJobTypes = []struct {
	label string
	cmd   string
}{
	{"URL — direct HTTP(S) link", daemon.CmdAddURL},
	{"Social/media — yt-dlp link", daemon.CmdAddSocial},
	{"Torrent — magnet link or .torrent file", daemon.CmdAddTorrent},
}

func (m statusModel) updateNewJob(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.newJob.step {
	case newJobPickType:
		switch msg.String() {
		case "up", "k":
			if m.newJob.typeIndex > 0 {
				m.newJob.typeIndex--
			}
		case "down", "j":
			if m.newJob.typeIndex < len(newJobTypes)-1 {
				m.newJob.typeIndex++
			}
		case "enter":
			if newJobTypes[m.newJob.typeIndex].cmd == daemon.CmdAddSocial {
				m.newJob.step = newJobPickPreset
			} else {
				m.newJob.step = newJobEnterLink
			}
		case "esc":
			m.newJob = nil
		}
		return m, nil

	case newJobPickPreset:
		switch msg.String() {
		case "up", "k":
			if m.newJob.presetIndex > 0 {
				m.newJob.presetIndex--
			}
		case "down", "j":
			if m.newJob.presetIndex < len(social.Presets)-1 {
				m.newJob.presetIndex++
			}
		case "enter":
			m.newJob.step = newJobEnterLink
		case "esc":
			m.newJob.step = newJobPickType
		}
		return m, nil

	default: // newJobEnterLink
		switch msg.String() {
		case "esc":
			if newJobTypes[m.newJob.typeIndex].cmd == daemon.CmdAddSocial {
				m.newJob.step = newJobPickPreset
			} else {
				m.newJob.step = newJobPickType
			}
			return m, nil
		case "enter":
			link := strings.TrimSpace(m.newJob.input.Value())
			if link == "" {
				return m, nil
			}
			apiCmd := newJobTypes[m.newJob.typeIndex].cmd
			format := ""
			if apiCmd == daemon.CmdAddSocial {
				format = social.Presets[m.newJob.presetIndex].Format
			}
			m.newJob = nil
			m.statusMsg = "starting..."
			return m, startNewJob(apiCmd, link, format)
		default:
			var cmd tea.Cmd
			m.newJob.input, cmd = m.newJob.input.Update(msg)
			return m, cmd
		}
	}
}

func (m statusModel) viewNewJob() string {
	switch m.newJob.step {
	case newJobPickType:
		var b strings.Builder
		b.WriteString(statStyle.Render("Start a new download — pick a type:"))
		b.WriteString("\n")
		for i, t := range newJobTypes {
			cursor := "  "
			if i == m.newJob.typeIndex {
				cursor = "> "
			}
			b.WriteString(cursor + t.label + "\n")
		}
		b.WriteString(helpStyle.Render("↑/↓ select  enter next  esc cancel"))
		return b.String()

	case newJobPickPreset:
		var b strings.Builder
		b.WriteString(statStyle.Render("Social/media — pick a quality preset:"))
		b.WriteString("\n")
		for i, p := range social.Presets {
			cursor := "  "
			if i == m.newJob.presetIndex {
				cursor = "> "
			}
			b.WriteString(fmt.Sprintf("%s%-8s %s\n", cursor, p.Name, p.Description))
		}
		b.WriteString(helpStyle.Render("↑/↓ select  enter next  esc back"))
		return b.String()

	default: // newJobEnterLink
		label := newJobTypes[m.newJob.typeIndex].label
		if newJobTypes[m.newJob.typeIndex].cmd == daemon.CmdAddSocial {
			label += " [" + social.Presets[m.newJob.presetIndex].Name + "]"
		}
		var b strings.Builder
		b.WriteString(statStyle.Render(fmt.Sprintf("%s — paste the link:", label)))
		b.WriteString("\n")
		b.WriteString(m.newJob.input.View())
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("enter start  esc back"))
		return b.String()
	}
}
