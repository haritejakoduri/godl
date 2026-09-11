package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/table"

	"godl/internal/daemon"
	"godl/internal/format"
	"godl/internal/store"
)

// statusColWidth is wider than the Status column strictly needs to be
// for its longest plain word ("completed", 9 chars) — see renderStatus's
// doc comment for why the extra room is load-bearing, not cosmetic.
const statusColWidth = 16

// fixedColumns are every table column except Path and Source, which grow
// or shrink with the terminal width instead of holding a constant size —
// see columnsForWidth. The first column has no header text: it's just
// each row's "[ ]"/"[x]" multi-select checkbox (see rebuildRows/the
// space key), which doesn't need a label to be self-explanatory.
var fixedColumns = []table.Column{
	{Title: "", Width: 3},
	{Title: "ID", Width: 9},
	{Title: "Type", Width: 8},
	{Title: "Status", Width: statusColWidth},
	{Title: "Progress", Width: 24},
	{Title: "Speed", Width: 12},
	{Title: "ETA", Width: 8},
}

// minPathWidth/minSourceWidth are floors for the two variable-width
// columns — narrow enough to still fit in a small terminal without the
// table itself needing to horizontally scroll.
const (
	minPathWidth   = 16
	minSourceWidth = 20
	numColumns     = 9 // fixedColumns + Path + Source
	// bubbles/table pads every cell 1 space on each side (see
	// table.DefaultStyles), on top of the column's own Width.
	perColumnPadding = 2
)

// columnsForWidth builds the table's columns for a terminal width chars
// wide, giving Path and Source (a local filesystem path and a URL/
// magnet/file source respectively — both often too long to fit in a
// fixed-width column without heavy truncation) whatever room is left
// over after the fixed columns and per-cell padding, instead of a
// constant width that either wastes space on a wide terminal or gets
// truncated hard on a narrow one. Source gets more of the extra room
// than Path, since links tend to run longer than local paths.
func columnsForWidth(width int) []table.Column {
	fixedWidth := 0
	for _, c := range fixedColumns {
		fixedWidth += c.Width
	}
	avail := width - fixedWidth - numColumns*perColumnPadding
	pathW, sourceW := minPathWidth, minSourceWidth
	if extra := avail - pathW - sourceW; extra > 0 {
		pathW += extra * 2 / 5
		sourceW += extra - extra*2/5
	}
	cols := make([]table.Column, 0, numColumns)
	cols = append(cols, fixedColumns...)
	cols = append(cols, table.Column{Title: "Path", Width: pathW}, table.Column{Title: "Source", Width: sourceW})
	return cols
}

// selectedJobError returns the currently-selected job's failure reason,
// or "" if it's not failed (or has none recorded) — see View()'s use
// of this for surfacing ErrorMsg without a fixed-width table column.
func (m statusModel) selectedJobError() string {
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.jobs) {
		return ""
	}
	j := m.jobs[idx]
	if j.Status != store.StatusFailed || j.ErrorMsg == "" {
		return ""
	}
	return fmt.Sprintf("%s failed: %s", j.ID, j.ErrorMsg)
}

// newestFirst returns jobs (store-ordered oldest-created-first) reversed,
// so the dashboard reads top-to-bottom the way you'd expect a live feed
// to: whatever you just started is right there, not pushed to the
// bottom behind everything already running.
func newestFirst(jobs []*daemon.JobView) []*daemon.JobView {
	out := make([]*daemon.JobView, len(jobs))
	for i, j := range jobs {
		out[len(jobs)-1-i] = j
	}
	return out
}

// cursorJobID returns the ID of the job currently under the cursor, or
// "" if there isn't one — used to re-find and re-focus the same job
// after a snapshot reorders the list (see the jobsMsg handler).
// cursorJob returns the job the table cursor is on, and its row index.
// ok is false when the table is empty or the cursor is out of range.
func (m statusModel) cursorJob() (job *daemon.JobView, idx int, ok bool) {
	idx = m.table.Cursor()
	if idx < 0 || idx >= len(m.jobs) {
		return nil, -1, false
	}
	return m.jobs[idx], idx, true
}

func (m statusModel) cursorJobID() string {
	j, _, ok := m.cursorJob()
	if !ok {
		return ""
	}
	return j.ID
}

// pruneSelected drops any selected job ID that's no longer in the
// current snapshot (removed by this client or another) — otherwise a
// stale ID lingers in the map forever, harmlessly but pointlessly.
func (m *statusModel) pruneSelected() {
	if len(m.selected) == 0 {
		return
	}
	live := make(map[string]bool, len(m.jobs))
	for _, j := range m.jobs {
		live[j.ID] = true
	}
	for id := range m.selected {
		if !live[id] {
			delete(m.selected, id)
		}
	}
}

// actionTargets returns the job IDs a pause/resume/cancel/retry/remove
// keypress should act on: every checked job if any are selected,
// otherwise just whatever's under the cursor — the same "selected, or
// current" convention webdavBrowseState's own multi-select already
// uses for its "d" (download) key.
func (m statusModel) actionTargets() []string {
	if len(m.selected) > 0 {
		ids := make([]string, 0, len(m.selected))
		for id := range m.selected {
			ids = append(ids, id)
		}
		return ids
	}
	idx := m.table.Cursor()
	if idx < 0 || idx >= len(m.jobs) {
		return nil
	}
	return []string{m.jobs[idx].ID}
}

// rebuildRows rebuilds the table's rows from m.jobs. cursorIdx is which
// row index is (about to be) the cursor row: that one renders Status
// as plain text rather than through renderStatus.
//
// This isn't a style choice — raw ANSI SGR codes don't nest through
// plain string concatenation the way markup would. renderStatus's
// closing sequence resets state unconditionally, so if a colored
// Status cell ends up inside the Selected style's row-wide wrapper
// (bubbles/table renders the whole joined row, then wraps *that* in
// Selected for the cursor's row), that reset kills the Selected
// style's bold/background for every cell after Status too — the
// cursor row's highlight visibly "cuts off" partway through instead
// of spanning the row. The cursor row's own highlight already marks
// it unambiguously, so skipping the redundant status color there
// costs nothing and sidesteps the corruption entirely rather than
// fighting raw ANSI nesting to preserve it.
func (m *statusModel) rebuildRows(cursorIdx int) {
	rows := make([]table.Row, 0, len(m.jobs))
	for i, j := range m.jobs {
		bar := m.bar.ViewAs(format.Percent(j.BytesDone, j.BytesTotal))
		check := "[ ]"
		if m.selected[j.ID] {
			check = "[x]"
		}
		status := string(j.Status)
		if i != cursorIdx {
			status = renderStatus(j.Status)
		}
		rows = append(rows, table.Row{
			check,
			j.ID,
			string(j.Type),
			status,
			bar,
			format.Speed(j.SpeedBps),
			format.ETA(j.ETASeconds, j.Status),
			format.ShortenHome(j.Output),
			format.ShortenHome(j.Source),
		})
	}
	m.table.SetRows(rows)
}

// renderStatus color-codes the Status cell. bubbles/table truncates via
// go-runewidth, which isn't ANSI-aware and counts escape sequences
// toward the width — truncate mid-sequence and the row is corrupted.
// statusColWidth is sized to clear that overcount (see
// TestRenderStatusFitsStatusColumn).
func renderStatus(status store.JobStatus) string {
	style, ok := jobStatusStyles[status]
	if !ok {
		return string(status)
	}
	return style.Render(string(status))
}
