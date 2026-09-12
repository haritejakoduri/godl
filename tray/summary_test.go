package tray

import (
	"strings"
	"testing"

	"godl/internal/daemon"
	"godl/internal/store"
)

func job(status store.JobStatus, speed float64) *daemon.JobView {
	return &daemon.JobView{Job: &store.Job{Status: status}, SpeedBps: speed}
}

func TestSummarizeCountsByStatus(t *testing.T) {
	s := summarize([]*daemon.JobView{
		job(store.StatusActive, 1000),
		job(store.StatusActive, 2000),
		job(store.StatusQueued, 0),
		job(store.StatusPaused, 0),
		job(store.StatusCompleted, 0),
		job(store.StatusFailed, 0),
	})
	if s.active != 2 || s.queued != 1 || s.paused != 1 {
		t.Errorf("active/queued/paused = %d/%d/%d, want 2/1/1", s.active, s.queued, s.paused)
	}
	// Completed and failed jobs must not contribute speed; they linger
	// in the list forever and would otherwise inflate the total.
	if s.speedBps != 3000 {
		t.Errorf("speedBps = %v, want 3000 (the two active jobs only)", s.speedBps)
	}
	if !s.daemonUp {
		t.Error("summarize should mark the daemon up — it only runs on a snapshot from a live one")
	}
}

func TestSummaryLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    summary
		want string
	}{
		{"no daemon", summary{}, "Daemon not running"},
		{"nothing to do", summary{daemonUp: true}, "Idle"},
		{"only finished jobs", summarize([]*daemon.JobView{job(store.StatusCompleted, 0)}), "Idle"},
		{"queued only, no speed", summary{daemonUp: true, queued: 3}, "3 queued"},
		{"paused only", summary{daemonUp: true, paused: 2}, "2 paused"},
		{"active shows speed", summary{daemonUp: true, active: 2, speedBps: 4_300_000}, "2 active — 4.1 MiB/s"},
		{"everything at once", summary{daemonUp: true, active: 1, queued: 5, paused: 2, speedBps: 1024}, "1 active, 5 queued, 2 paused — 1.0 KiB/s"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.line(); got != tc.want {
				t.Errorf("line() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestQueuedOnlyOmitsSpeed: a queue waiting its turn is not a stall, but
// "5 queued — 0 B/s" reads like one.
func TestQueuedOnlyOmitsSpeed(t *testing.T) {
	if got := (summary{daemonUp: true, queued: 5}).line(); strings.Contains(got, "B/s") {
		t.Errorf("line() = %q, want no speed when nothing is active", got)
	}
}

func TestTooltipNamesTheApp(t *testing.T) {
	got := (summary{daemonUp: true, active: 1, speedBps: 500}).tooltip()
	if !strings.HasPrefix(got, "godl — ") {
		t.Errorf("tooltip() = %q, want it to lead with the app name", got)
	}
}
