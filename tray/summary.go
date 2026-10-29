package tray

import (
	"fmt"
	"strings"

	"godl/internal/daemon"
	"godl/internal/format"
	"godl/internal/store"
)

// summary is everything the icon and its header line show, reduced from
// one snapshot of the job list. Kept separate from the tray plumbing so
// what the user actually reads can be tested on any platform, including
// the ones that have no tray at all.
type summary struct {
	daemonUp bool
	active   int
	queued   int
	paused   int
	speedBps float64
}

func summarize(jobs []*daemon.JobView) summary {
	s := summary{daemonUp: true}
	for _, j := range jobs {
		switch j.Status {
		case store.StatusActive:
			s.active++
			s.speedBps += j.SpeedBps
		case store.StatusQueued:
			s.queued++
		case store.StatusPaused:
			s.paused++
		}
	}
	return s
}

// line is the menu's header: a short, glanceable state of the world.
func (s summary) line() string {
	if !s.daemonUp {
		return "Daemon not running"
	}
	var parts []string
	if s.active > 0 {
		parts = append(parts, fmt.Sprintf("%d active", s.active))
	}
	if s.queued > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", s.queued))
	}
	if s.paused > 0 {
		parts = append(parts, fmt.Sprintf("%d paused", s.paused))
	}
	if len(parts) == 0 {
		return "Idle"
	}
	line := strings.Join(parts, ", ")
	// Speed only when something is actually moving: "0 B/s" next to a
	// queue that is merely waiting on its turn reads like a stall.
	if s.active > 0 {
		line += " — " + format.Speed(s.speedBps)
	}
	return line
}

// tooltip is what a hover shows, so it names the app; the menu header
// has godl's name above it already and doesn't need to repeat it.
func (s summary) tooltip() string { return "godl — " + s.line() }
