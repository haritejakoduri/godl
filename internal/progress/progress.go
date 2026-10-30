// Package progress turns a byte count that is still moving into lines
// something else can print.
//
// It exists because godl's installers hand their caller a plain
// func(string): a terminal, a job log and a test all take lines, and
// none of them wants a different interface. So the bar is rendered
// here, as text, and whoever receives it decides whether to repaint it
// in place or write it down.
//
// Rate is the other half. A download emits a read every few kilobytes;
// forwarding all of them would be unreadable in a terminal and
// enormous in a log. Lines come at most every interval, with the last
// one always sent so a transfer never stops short of the end it
// actually reached.
package progress

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"godl/internal/format"
)

const (
	// interval is the shortest gap between two lines. Fast enough to
	// look live, slow enough to read.
	interval = 400 * time.Millisecond

	// barWidth is in characters, chosen to leave room for the counts
	// and speed beside it inside 80 columns.
	barWidth = 24
)

// Reporter renders one transfer. The zero value is not usable; use New.
type Reporter struct {
	what  string
	emit  func(string)
	start time.Time

	mu   sync.Mutex
	last time.Time
	sent bool
}

// New returns a Reporter that describes its transfer as what ("ffmpeg",
// "yt-dlp") and sends lines to emit. A nil emit is allowed and makes
// every method a no-op, so callers that have nowhere to report need no
// special case.
func New(what string, emit func(string)) *Reporter {
	return &Reporter{what: what, emit: emit, start: time.Now()}
}

// Report records progress. done is bytes transferred so far; total is
// the expected size, or 0 when the server did not say — in which case
// the line shows what has arrived without pretending to know how much
// is left.
//
// Safe to call from any goroutine and as often as the transfer likes.
func (r *Reporter) Report(done, total int64) {
	if r == nil || r.emit == nil {
		return
	}
	r.mu.Lock()
	finished := total > 0 && done >= total
	if !finished && r.sent && time.Since(r.last) < interval {
		r.mu.Unlock()
		return
	}
	r.last = time.Now()
	r.sent = true
	elapsed := time.Since(r.start).Seconds()
	r.mu.Unlock()

	r.emit(line(r.what, done, total, elapsed))
}

// Func adapts Report to the callback shape the downloaders take.
func (r *Reporter) Func() func(done, total int64) {
	if r == nil || r.emit == nil {
		return nil
	}
	return r.Report
}

// line is the rendering, kept apart from the throttling so it can be
// tested without waiting for a clock.
func line(what string, done, total int64, elapsed float64) string {
	var speed string
	if elapsed > 0 {
		speed = "  " + format.Speed(float64(done)/elapsed)
	}
	if total <= 0 {
		// No Content-Length. Showing a bar here would mean inventing a
		// denominator, and a bar that is secretly a guess is worse
		// than no bar.
		return fmt.Sprintf("%s  %s%s", what, format.Bytes(done), speed)
	}
	// format.Percent returns a 0..1 fraction, not a percentage — the
	// same multiply cmd/list.go does.
	return fmt.Sprintf("%s  %s %3.0f%%  %s / %s%s",
		what, Bar(done, total, barWidth), format.Percent(done, total)*100,
		format.Bytes(done), format.Bytes(total), speed)
}

// Bar draws the bar itself. Exported for the tests, and because it is
// the part most likely to be wanted somewhere else.
func Bar(done, total int64, width int) string {
	if width < 2 {
		return ""
	}
	filled := 0
	if total > 0 {
		filled = int(float64(width) * float64(done) / float64(total))
	}
	// A transfer reporting more than it promised (a server that lied
	// about Content-Length) must not run off the end of the bar.
	filled = min(max(filled, 0), width)
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", width-filled) + "]"
}
