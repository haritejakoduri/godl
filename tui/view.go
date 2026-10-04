package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"

	"godl/internal/format"
	"godl/internal/store"
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
	case m.serve != nil:
		return m.viewServe()
	case m.details != nil:
		return m.viewDetails()
	}

	return m.dashboardHeader() + "\n" + m.table.View() + "\n" + m.dashboardFooter()
}

const dashboardHelp = "space select  p pause  r resume  x cancel  R retry  d remove  D remove+delete  o play  i details  t sort  n new  w webdav  s settings  S serve  ↑/↓ move  q quit"

// dashboardMessageLines is how many message lines (connection error,
// status/job error) the table leaves room for beside the help text, so
// the table only resizes when a message outgrows that, not every time
// one appears.
const dashboardMessageLines = 2

var (
	brandStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0B0B0B")).Background(lipgloss.Color("#5FD6C9")).Padding(0, 1)
	headerDimStyle  = lipgloss.NewStyle().Faint(true)
	headerSpinStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#5FD6C9")).Bold(true)
	headerRateStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#5FD6C9"))
)

// overallBar is the header's combined progress bar. It's outside the
// table, so unlike the per-row bars it can be drawn in color.
var overallBar = progress.New(progress.WithGradient("#5FD6C9", "#7B61FF"), progress.WithWidth(20))

// dashboardHeader is the live title line: a spinner while anything
// runs, counts by state, the combined download speed with its recent
// history, and one bar for everything still in flight.
func (m statusModel) dashboardHeader() string {
	counts := map[store.JobStatus]int{}
	for _, j := range m.jobs {
		counts[j.Status]++
	}
	frame := m.anim.frameNo()

	parts := []string{brandStyle.Render("godl")}
	if counts[store.StatusActive] > 0 {
		parts = append(parts, headerSpinStyle.Render(spinnerFrames[frame%len(spinnerFrames)]))
	}
	stats := []string{fmt.Sprintf("%d job(s)", len(m.jobs))}
	for _, s := range []store.JobStatus{store.StatusActive, store.StatusQueued, store.StatusSeeding, store.StatusPaused, store.StatusFailed, store.StatusCompleted} {
		if n := counts[s]; n > 0 {
			stats = append(stats, jobStatusStyles[s].Render(fmt.Sprintf("%d %s", n, s)))
		}
	}
	parts = append(parts, strings.Join(stats, headerDimStyle.Render(" · ")))

	if speed := m.headerSpeed(); speed > 0 {
		rate := "↓ " + format.Speed(speed)
		if m.anim != nil {
			if spark := sparkline(m.anim.totalSpeeds, 8); spark != "" {
				rate = spark + " " + rate
			}
		}
		parts = append(parts, headerRateStyle.Render(rate))
	}
	if frac, ok := m.overallProgress(); ok {
		parts = append(parts, overallBar.ViewAs(frac))
	}
	if len(m.selected) > 0 {
		parts = append(parts, fmt.Sprintf("(%d selected)", len(m.selected)))
	}
	if m.sortBy != sortNewest || m.sortRev {
		order := "sorted by " + m.sortBy.String()
		if m.sortRev {
			order += " ↕"
		}
		parts = append(parts, headerDimStyle.Render(order))
	}
	return m.wrapped(titleStyle).Render(strings.Join(parts, "  "))
}

// headerSpeed is the combined download speed, eased when animating.
func (m statusModel) headerSpeed() float64 {
	if m.anim != nil && !m.anim.off {
		return m.anim.speedShown
	}
	return totalSpeed(m.jobs)
}

// overallProgress is how far along everything still in flight is,
// weighted by size, using each bar's animated position so the header
// moves in step with the rows. ok is false when nothing is in flight.
func (m statusModel) overallProgress() (float64, bool) {
	var done, total float64
	for _, j := range m.jobs {
		if j.Status != store.StatusActive && j.Status != store.StatusQueued || j.BytesTotal <= 0 {
			continue
		}
		total += float64(j.BytesTotal)
		done += m.anim.shownFraction(j) * float64(j.BytesTotal)
	}
	if total == 0 {
		return 0, false
	}
	return done / total, true
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
	case m.selectedJobPhase() != "":
		// What TorBox is doing with the selected torrent, while nothing
		// has reached this machine yet.
		lines = append(lines, m.wrapped(statStyle).Render(m.selectedJobPhase()))
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

// selectedJobPhase is the cursor job's TorBox progress, if it has one.
func (m statusModel) selectedJobPhase() string {
	j, _, ok := m.cursorJob()
	if !ok || j.Status != store.StatusActive || !strings.HasPrefix(j.Phase, "TorBox") {
		return ""
	}
	return j.Phase
}
