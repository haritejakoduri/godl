package tui

import (
	"fmt"
	"strings"
)

// View renders the job table (the default screen) unless a full-screen
// overlay — new-job wizard, WebDAV browser, or settings — is active. An
// overlay replaces the whole screen rather than being appended below
// the job table and its own footer: the job table's height tracks the
// terminal's (see Update's WindowSizeMsg case) and can run to dozens
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

	var b strings.Builder
	title := fmt.Sprintf("godl status — %d job(s)", len(m.jobs))
	if len(m.selected) > 0 {
		title += fmt.Sprintf("  (%d selected)", len(m.selected))
	}
	b.WriteString(titleStyle.Render(title))
	b.WriteString("\n")
	b.WriteString(m.table.View())
	b.WriteString("\n")
	if m.err != nil {
		b.WriteString(errStyle.Render("daemon connection error: " + m.err.Error()))
		b.WriteString("\n")
	}
	switch {
	case m.statusMsg != "":
		b.WriteString(statStyle.Render(m.statusMsg))
		b.WriteString("\n")
	case m.selectedJobError() != "":
		// The jobs table's Status column is too narrow to show a failure
		// reason, so a failed row's ErrorMsg shows here instead, just by
		// scrolling to it — no extra keybinding needed.
		b.WriteString(errStyle.Render(m.selectedJobError()))
		b.WriteString("\n")
	}
	b.WriteString(helpStyle.Render("space select  p pause  r resume  x cancel  R retry  d remove  D remove+delete file  o play/stream  n new download  w browse webdav  s settings  ↑/↓ navigate  q quit"))
	return b.String()
}
