package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// View renders the job table (the default screen) unless a full-screen
// overlay — new-job wizard, WebDAV browser, or settings — is active. An
// overlay replaces the whole screen rather than being appended below
// the job table and its own footer: the job table's height tracks the
// terminal's (see fitTable) and can run to dozens
// of rows, which previously left an overlay's own list — the WebDAV
// browser's especially, with potentially hundreds of remote entries —
// squeezed into whatever space remained below it, or pushed off-screen
// entirely with two conflicting footers on screen at once. Each
// overlay's own view already ends with its own contextual footer, so
// nothing else needs to be appended for those cases.
func (m statusModel) View() string {
	switch {
	case m.newJob != nil:
		return m.viewNewJob()
	case m.webdavBrowse != nil:
		return m.viewWebDAVBrowse()
	case m.settings != nil:
		return m.viewSettings()
	}

	return m.dashboardHeader() + "\n" + m.table.View() + "\n" + m.dashboardFooter()
}

const dashboardHelp = "space select  p pause  r resume  x cancel  R retry  d remove  D remove+delete file  o play/stream  n new download  w browse webdav  s settings  ↑/↓ navigate  q quit"

// dashboardMessageLines is how many message lines (connection error,
// status/job error) the table leaves room for beside the help text, so
// the table only resizes when a message outgrows that, not every time
// one appears.
const dashboardMessageLines = 2

func (m statusModel) dashboardHeader() string {
	title := fmt.Sprintf("godl status — %d job(s)", len(m.jobs))
	if len(m.selected) > 0 {
		title += fmt.Sprintf("  (%d selected)", len(m.selected))
	}
	return m.wrapped(titleStyle).Render(title)
}

// dashboardFooter is everything under the table: the connection error,
// then a status message or the selected job's failure, then the help.
func (m statusModel) dashboardFooter() string {
	var lines []string
	if m.err != nil {
		lines = append(lines, m.wrapped(errStyle).Render("daemon connection error: "+m.err.Error()))
	}
	switch {
	case m.statusMsg != "":
		lines = append(lines, m.wrapped(statStyle).Render(m.statusMsg))
	case m.selectedJobError() != "":
		// The jobs table's Status column is too narrow to show a failure
		// reason, so a failed row's ErrorMsg shows here instead, just by
		// scrolling to it — no extra keybinding needed.
		lines = append(lines, m.wrapped(errStyle).Render(m.selectedJobError()))
	}
	lines = append(lines, m.helpView(dashboardHelp))
	return strings.Join(lines, "\n")
}

// fitTable sizes the table to the room left under the header and footer.
// The footer wraps to the terminal width, so its height — and therefore
// the table's — depends on the width and on what's currently showing;
// a fixed allowance let a narrow terminal push the title off the top.
// Only ever grows the footer's reservation past the usual case, so the
// table doesn't jump as ordinary messages come and go.
func (m *statusModel) fitTable() {
	if m.height <= 0 {
		return
	}
	usual := lipgloss.Height(m.helpView(dashboardHelp)) + dashboardMessageLines
	footer := max(lipgloss.Height(m.dashboardFooter()), usual)
	const slack = 1
	h := m.height - lipgloss.Height(m.dashboardHeader()) - footer - slack
	if h > 3 && h != m.tableHeight {
		m.tableHeight = h
		m.table.SetHeight(h)
	}
}

// wrapped returns style constrained to the terminal width, so long text
// wraps onto more lines instead of being cut off at the screen edge.
// Before the first WindowSizeMsg it's style unchanged.
func (m statusModel) wrapped(style lipgloss.Style) lipgloss.Style {
	if m.width <= 0 {
		return style
	}
	return style.Width(m.width)
}

// helpView renders a help/footer hint, wrapped to the terminal width.
func (m statusModel) helpView(s string) string {
	return m.wrapped(helpStyle).Render(s)
}
