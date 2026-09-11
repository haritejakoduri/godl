// Package format renders values for people to read: byte counts,
// transfer rates, remaining time, progress fractions, and paths.
//
// It's deliberately free of any dependency on a particular front end.
// These helpers started out inside the cobra command package, which
// meant the TUI had to reach into it to display anything — and a future
// GUI would have had to do the same. Keeping them here lets every front
// end present the same numbers the same way without depending on
// another one.
package format

import (
	"fmt"
	"os"
	"strings"
	"time"

	"godl/internal/store"
)

// Bytes renders a byte count in binary units, or "?" for a negative
// count (godl uses -1 for "the server didn't tell us").
func Bytes(n int64) string {
	if n < 0 {
		return "?"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// Speed renders a transfer rate, or "-" when there isn't one to show.
func Speed(bps float64) string {
	if bps <= 0 {
		return "-"
	}
	return Bytes(int64(bps)) + "/s"
}

// ETA renders the time remaining. Only an active job has one: a paused
// or finished job's last computed estimate is stale and showing it would
// imply the job is still moving.
func ETA(seconds int64, status store.JobStatus) string {
	if status != store.StatusActive || seconds < 0 {
		return "-"
	}
	d := time.Duration(seconds) * time.Second
	if d > 99*time.Hour {
		return ">99h"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// Percent returns done/total clamped to [0,1], and 0 when the total
// isn't known yet.
func Percent(done, total int64) float64 {
	if total <= 0 {
		return 0
	}
	p := float64(done) / float64(total)
	if p > 1 {
		p = 1
	}
	if p < 0 {
		p = 0
	}
	return p
}

// Truncate shortens s to at most n characters, marking the cut with an
// ellipsis. Counted in runes, not bytes, so a non-ASCII name is cut at a
// character boundary rather than mid-encoding.
func Truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// ShortenHome renders p with the user's home directory replaced by "~",
// the way most CLI tools display a path back to the user —
// "/home/alice/Downloads" reads as noise next to "~/Downloads". Falls
// back to p unchanged if the home directory can't be determined or
// isn't actually a prefix of p.
func ShortenHome(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(p, home+string(os.PathSeparator)); ok {
		return "~" + string(os.PathSeparator) + rest
	}
	return p
}
