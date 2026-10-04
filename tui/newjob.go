package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"godl/internal/daemon"
	"godl/internal/ratelimit"
	"godl/internal/social"
)

type newJobStep int

const (
	newJobPickType   newJobStep = iota
	newJobPickPreset            // social only — skipped for url/torrent
	newJobEnterLink
	newJobPickFiles   // torrent only — which of its files to download
	newJobEnterOutput // optional — blank keeps the CLI's own default
	newJobEnterRate   // optional — blank means unlimited
)

type newJobState struct {
	step        newJobStep
	typeIndex   int
	presetIndex int // into social.Presets, social jobs only
	input       textinput.Model
	outputInput textinput.Model
	rateInput   textinput.Model
	err         string // set on an invalid rate limit, cleared on the next edit
	pick        *torrentPick
}

// newJobPlay is the wizard's one entry that isn't a daemon command:
// stream the link in a player (see playLink) instead of downloading it.
const newJobPlay = "play"

// newJobTypes are the job types the TUI can start directly, in the
// order they're offered — mirroring the CLI's url/social/torrent/play
// subcommands.
var newJobTypes = []struct {
	label string
	cmd   string
}{
	{"URL — direct HTTP(S) link", daemon.CmdAddURL},
	{"Social/media — yt-dlp link", daemon.CmdAddSocial},
	{"Torrent — magnet link or .torrent file", daemon.CmdAddTorrent},
	{"Play — stream a yt-dlp link in mpv/VLC, no download", newJobPlay},
}

// newJobHasPresets reports whether apiCmd's wizard path starts with
// the quality-preset menu: the two yt-dlp-backed types.
func newJobHasPresets(apiCmd string) bool {
	return apiCmd == daemon.CmdAddSocial || apiCmd == newJobPlay
}

// selectedFormat is the chosen preset's yt-dlp format selector, or ""
// for a type that has none.
func (s *newJobState) selectedFormat() string {
	if !newJobHasPresets(newJobTypes[s.typeIndex].cmd) {
		return ""
	}
	return social.Presets[s.presetIndex].Format
}

// newJobOutputLabel returns the wizard's output-step prompt, worded to
// match what apiCmd actually does with it: a url job's -o is a full
// file path, while social/torrent treat it as a destination directory
// (see cmd/url.go vs cmd/social.go/torrent.go's own --output help text).
func newJobOutputLabel(apiCmd string) string {
	if apiCmd == daemon.CmdAddURL {
		return "Output file path (blank = your Downloads folder, name from the URL):"
	}
	return "Output directory (blank = your Downloads folder):"
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
			if newJobHasPresets(newJobTypes[m.newJob.typeIndex].cmd) {
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

	case newJobEnterLink:
		switch msg.String() {
		case "esc":
			if newJobHasPresets(newJobTypes[m.newJob.typeIndex].cmd) {
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
			// Nothing is saved or rate-limited when only playing, so
			// the two steps after this one don't apply.
			if newJobTypes[m.newJob.typeIndex].cmd == newJobPlay {
				format := m.newJob.selectedFormat()
				m.newJob = nil
				m.statusMsg = "opening player..."
				return m, playLink(link, format)
			}
			if newJobTypes[m.newJob.typeIndex].cmd == daemon.CmdAddTorrent {
				pick, cmd := newTorrentPick(link)
				m.newJob.pick = pick
				m.newJob.step = newJobPickFiles
				return m, cmd
			}
			return m.advanceToOutput(), nil
		default:
			var cmd tea.Cmd
			m.newJob.input, cmd = m.newJob.input.Update(msg)
			return m, cmd
		}

	case newJobPickFiles:
		return m.updateTorrentPick(msg)

	case newJobEnterOutput:
		switch msg.String() {
		case "esc":
			m.newJob.step = newJobEnterLink
			if m.newJob.pick != nil && len(m.newJob.pick.files) > 1 {
				m.newJob.step = newJobPickFiles
			}
			return m, nil
		case "enter":
			ti := textinput.New()
			ti.Placeholder = "unlimited"
			ti.CharLimit = 16
			ti.Width = 24
			ti.Focus()
			m.newJob.rateInput = ti
			m.newJob.step = newJobEnterRate
			return m, nil
		default:
			var cmd tea.Cmd
			m.newJob.outputInput, cmd = m.newJob.outputInput.Update(msg)
			return m, cmd
		}

	default: // newJobEnterRate
		switch msg.String() {
		case "esc":
			m.newJob.err = ""
			m.newJob.step = newJobEnterOutput
			return m, nil
		case "enter":
			rateStr := strings.TrimSpace(m.newJob.rateInput.Value())
			var limitRate int64
			if rateStr != "" {
				lr, err := ratelimit.ParseRate(rateStr)
				if err != nil {
					m.newJob.err = err.Error()
					return m, nil
				}
				limitRate = lr
			}
			link := strings.TrimSpace(m.newJob.input.Value())
			output := strings.TrimSpace(m.newJob.outputInput.Value())
			apiCmd := newJobTypes[m.newJob.typeIndex].cmd
			format := m.newJob.selectedFormat()
			files := m.newJob.pick.spec()
			m.newJob = nil
			m.statusMsg = "starting..."
			return m, startNewJob(apiCmd, link, format, output, limitRate, files)
		default:
			m.newJob.err = ""
			var cmd tea.Cmd
			m.newJob.rateInput, cmd = m.newJob.rateInput.Update(msg)
			return m, cmd
		}
	}
}

func (m statusModel) viewNewJob() string {
	switch m.newJob.step {
	case newJobPickType:
		var b strings.Builder
		b.WriteString(m.wrapped(statStyle).Render("Start a new download — pick a type:"))
		b.WriteString("\n")
		for i, t := range newJobTypes {
			cursor := "  "
			if i == m.newJob.typeIndex {
				cursor = "> "
			}
			b.WriteString(cursor + t.label + "\n")
		}
		b.WriteString(m.helpView("↑/↓ select  enter next  esc cancel"))
		return b.String()

	case newJobPickPreset:
		var b strings.Builder
		title := "Social/media — pick a quality preset:"
		if newJobTypes[m.newJob.typeIndex].cmd == newJobPlay {
			title = "Play — pick a quality preset:"
		}
		b.WriteString(m.wrapped(statStyle).Render(title))
		b.WriteString("\n")
		for i, p := range social.Presets {
			cursor := "  "
			if i == m.newJob.presetIndex {
				cursor = "> "
			}
			b.WriteString(fmt.Sprintf("%s%-8s %s\n", cursor, p.Name, p.Description))
		}
		b.WriteString(m.helpView("↑/↓ select  enter next  esc back"))
		return b.String()

	case newJobEnterLink:
		apiCmd := newJobTypes[m.newJob.typeIndex].cmd
		label := newJobTypes[m.newJob.typeIndex].label
		if newJobHasPresets(apiCmd) {
			label += " [" + social.Presets[m.newJob.presetIndex].Name + "]"
		}
		help := "enter next  esc back"
		if apiCmd == newJobPlay {
			help = "enter play  esc back"
		}
		var b strings.Builder
		b.WriteString(m.wrapped(statStyle).Render(fmt.Sprintf("%s — paste the link:", label)))
		b.WriteString("\n")
		b.WriteString(m.newJob.input.View())
		b.WriteString("\n")
		b.WriteString(m.helpView(help))
		return b.String()

	case newJobPickFiles:
		return m.viewTorrentPick()

	case newJobEnterOutput:
		apiCmd := newJobTypes[m.newJob.typeIndex].cmd
		var b strings.Builder
		b.WriteString(m.wrapped(statStyle).Render(newJobOutputLabel(apiCmd)))
		b.WriteString("\n")
		b.WriteString(m.newJob.outputInput.View())
		b.WriteString("\n")
		b.WriteString(m.helpView("enter next (blank = default)  esc back"))
		return b.String()

	default: // newJobEnterRate
		var b strings.Builder
		b.WriteString(m.wrapped(statStyle).Render(`Rate limit for this job, e.g. "2M" or "500K" (blank = unlimited):`))
		b.WriteString("\n")
		b.WriteString(m.newJob.rateInput.View())
		b.WriteString("\n")
		if m.newJob.err != "" {
			b.WriteString(m.wrapped(errStyle).Render("error: " + m.newJob.err))
			b.WriteString("\n")
		}
		b.WriteString(m.helpView("enter start  esc back"))
		return b.String()
	}
}

// advanceToOutput moves the wizard on to the optional output step.
func (m statusModel) advanceToOutput() statusModel {
	ti := textinput.New()
	ti.Placeholder = "default"
	ti.CharLimit = 4096
	ti.Width = 60
	ti.Focus()
	m.newJob.outputInput = ti
	m.newJob.step = newJobEnterOutput
	return m
}
