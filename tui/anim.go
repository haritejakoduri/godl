package tui

import (
	"math"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/harmonica"

	"godl/internal/daemon"
	"godl/internal/format"
	"godl/internal/store"
)

// The dashboard animates between the daemon's twice-a-second snapshots:
// bars glide to their new value on a spring instead of jumping, active
// rows spin, a highlight sweeps along running bars, and speed history
// draws as a sparkline. Frames only tick while something is moving, so
// an idle dashboard still costs nothing (see needsFrames).
//
// Table cells can't carry color — bubbles/table truncates with an
// ANSI-unaware width count (see renderStatus) — so the motion in the
// table is all glyphs; color lives in the header, which isn't a cell.

const animFPS = 15

var frameInterval = time.Second / animFPS

type animTickMsg time.Time

func animTick() tea.Cmd {
	return tea.Tick(frameInterval, func(t time.Time) tea.Msg { return animTickMsg(t) })
}

// reducedMotion turns every animation off: values snap, nothing spins,
// and no frames tick. For slow links (SSH over a bad connection) or
// anyone who'd rather not.
func reducedMotion() bool {
	v := os.Getenv("GODL_NO_ANIMATION")
	return (v != "" && v != "0") || os.Getenv("TERM") == "dumb"
}

// progressSpring is critically damped: bars ease into place without
// overshooting past the real value, which would read as progress lost.
var progressSpring = harmonica.NewSpring(harmonica.FPS(animFPS), 9.0, 1.0)

// speedHistoryLen is how many snapshots of speed the sparkline shows —
// about the last five seconds.
const speedHistoryLen = 10

// completionFlash is how long a newly completed row celebrates.
const completionFlash = 1500 * time.Millisecond

// jobAnim is one job's animation state, carried across snapshots.
type jobAnim struct {
	shown, vel float64 // displayed fraction [0,1] and its spring velocity
	target     float64
	speeds     []float64
	lastStatus store.JobStatus
	flashUntil time.Time
}

// animState is shared by value copies of statusModel (it's a pointer),
// which is what bubbletea's copy-per-Update model needs.
type animState struct {
	jobs    map[string]*jobAnim
	frame   int
	ticking bool
	off     bool
	now     func() time.Time

	// Aggregate download speed for the header, eased like the bars,
	// and its recent history for the header's sparkline.
	speedShown, speedVel float64
	totalSpeeds          []float64
}

func newAnimState() *animState {
	return &animState{jobs: map[string]*jobAnim{}, off: reducedMotion(), now: time.Now}
}

// observe records a fresh snapshot: new targets for every bar, a speed
// sample for each running job, and a completion flash for any job that
// just finished. Jobs no longer listed are forgotten.
func (a *animState) observe(jobs []*daemon.JobView) {
	if a == nil {
		return
	}
	now := a.now()
	live := make(map[string]bool, len(jobs))
	for _, j := range jobs {
		live[j.ID] = true
		ja, ok := a.jobs[j.ID]
		target := format.Percent(j.BytesDone, j.BytesTotal)
		if j.Status == store.StatusCompleted || j.Status == store.StatusSeeding {
			target = 1
		}
		if !ok {
			// First sight: start where the job already is, so opening
			// the dashboard doesn't replay every bar from zero.
			ja = &jobAnim{shown: target, lastStatus: j.Status}
			a.jobs[j.ID] = ja
		}
		ja.target = target
		if a.off {
			ja.shown, ja.vel = target, 0
		}
		if j.Status == store.StatusActive {
			ja.speeds = append(ja.speeds, j.SpeedBps)
			if len(ja.speeds) > speedHistoryLen {
				ja.speeds = ja.speeds[len(ja.speeds)-speedHistoryLen:]
			}
		} else if j.Status != store.StatusSeeding {
			ja.speeds = nil
		}
		if j.Status == store.StatusCompleted && ja.lastStatus != store.StatusCompleted && ok && !a.off {
			ja.flashUntil = now.Add(completionFlash)
		}
		ja.lastStatus = j.Status
	}
	for id := range a.jobs {
		if !live[id] {
			delete(a.jobs, id)
		}
	}
	if total := totalSpeed(jobs); total > 0 {
		a.totalSpeeds = append(a.totalSpeeds, total)
		if len(a.totalSpeeds) > speedHistoryLen {
			a.totalSpeeds = a.totalSpeeds[len(a.totalSpeeds)-speedHistoryLen:]
		}
	} else {
		a.totalSpeeds = nil
	}
	if a.off {
		a.speedShown, a.speedVel = totalSpeed(jobs), 0
	}
}

// step advances every spring by one frame.
func (a *animState) step(jobs []*daemon.JobView) {
	a.frame++
	for _, ja := range a.jobs {
		ja.shown, ja.vel = progressSpring.Update(ja.shown, ja.vel, ja.target)
		if math.Abs(ja.target-ja.shown) < 0.0005 && math.Abs(ja.vel) < 0.0005 {
			ja.shown, ja.vel = ja.target, 0
		}
		ja.shown = min(max(ja.shown, 0), 1)
	}
	target := totalSpeed(jobs)
	a.speedShown, a.speedVel = progressSpring.Update(a.speedShown, a.speedVel, target)
	if math.Abs(target-a.speedShown) < 1 {
		a.speedShown, a.speedVel = target, 0
	}
}

// needsFrames reports whether anything is still moving: a running job
// (spinner, sweep), a bar or the header speed still easing, or a flash.
func (a *animState) needsFrames(jobs []*daemon.JobView) bool {
	if a == nil || a.off {
		return false
	}
	for _, j := range jobs {
		switch j.Status {
		case store.StatusActive, store.StatusSeeding:
			return true
		}
	}
	now := a.now()
	for _, ja := range a.jobs {
		if ja.shown != ja.target || now.Before(ja.flashUntil) {
			return true
		}
	}
	return a.speedShown != totalSpeed(jobs)
}

// kick starts the frame loop if it's needed and not already running.
// Exactly one tick is ever in flight; the tick handler re-arms it.
func (a *animState) kick(jobs []*daemon.JobView) tea.Cmd {
	if a == nil || a.ticking || !a.needsFrames(jobs) {
		return nil
	}
	a.ticking = true
	return animTick()
}

// shownFraction is what a job's bar should show this frame.
func (a *animState) shownFraction(j *daemon.JobView) float64 {
	if a != nil {
		if ja, ok := a.jobs[j.ID]; ok {
			return ja.shown
		}
	}
	if j.Status == store.StatusCompleted || j.Status == store.StatusSeeding {
		return 1
	}
	return format.Percent(j.BytesDone, j.BytesTotal)
}

func (a *animState) flashing(id string) bool {
	if a == nil {
		return false
	}
	ja, ok := a.jobs[id]
	return ok && a.now().Before(ja.flashUntil)
}

func (a *animState) speeds(id string) []float64 {
	if a == nil {
		return nil
	}
	if ja, ok := a.jobs[id]; ok {
		return ja.speeds
	}
	return nil
}

// frameNo is the current frame, or 0 with animation off — every glyph
// then holds its first, static pose.
func (a *animState) frameNo() int {
	if a == nil || a.off {
		return 0
	}
	return a.frame
}

func totalSpeed(jobs []*daemon.JobView) float64 {
	var sum float64
	for _, j := range jobs {
		if j.Status == store.StatusActive {
			sum += j.SpeedBps
		}
	}
	return sum
}

var (
	spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	seedFrames    = []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	queueFrames   = []string{"⠁", "⠂", "⠄", "⡀", "⢀", "⠠", "⠐", "⠈"}
	flashFrames   = []string{"✦", "✧", "✦", "✧", "*"}
	eighths       = []string{"", "▏", "▎", "▍", "▌", "▋", "▊", "▉"}
	sparkLevels   = []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}
)

// statusIcon is the one-cell glyph in front of a job's status word.
// Running states animate; settled ones hold still.
func statusIcon(status store.JobStatus, frame int, flash bool) string {
	switch status {
	case store.StatusActive:
		return spinnerFrames[frame%len(spinnerFrames)]
	case store.StatusSeeding:
		return seedFrames[(frame/2)%len(seedFrames)]
	case store.StatusQueued:
		return queueFrames[(frame/3)%len(queueFrames)]
	case store.StatusCompleted:
		if flash {
			return flashFrames[(frame/2)%len(flashFrames)]
		}
		return "✓"
	case store.StatusFailed:
		return "✗"
	case store.StatusPaused:
		return "="
	default:
		return "-"
	}
}

// statusLabel is a job's full Status cell text, before any color.
func statusLabel(status store.JobStatus, frame int, flash bool) string {
	return statusIcon(status, frame, flash) + " " + string(status)
}

// barWidth is the progress bar's own width in cells, leaving room in
// the Progress column for " 100%".
const barWidth = 18

// renderBar draws a progress bar to eighth-of-a-cell precision, so a
// slowly moving download still visibly creeps forward. A running job
// gets a highlight sweeping along its filled part; one with no known
// size gets a block bouncing end to end instead of a bar stuck at 0%.
func renderBar(frac float64, width, frame int, status store.JobStatus, sizeKnown bool) string {
	if status == store.StatusActive && !sizeKnown {
		return bounceBar(width, frame)
	}
	frac = min(max(frac, 0), 1)
	eighthsFilled := int(math.Round(frac * float64(width*8)))
	full := eighthsFilled / 8
	partial := eighthsFilled % 8

	cells := make([]string, 0, width)
	for i := 0; i < full; i++ {
		cells = append(cells, "█")
	}
	if partial > 0 && full < width {
		cells = append(cells, eighths[partial])
	}
	for len(cells) < width {
		cells = append(cells, "░")
	}

	if status == store.StatusActive && full > 2 {
		// The sweep runs a few cells past the end so it reads as
		// passing through rather than bouncing off the fill's edge.
		pos := frame % (full + 4)
		if pos < full {
			cells[pos] = "▓"
		}
	}
	return strings.Join(cells, "")
}

// bounceBar is the indeterminate bar: a 3-cell block sliding back and
// forth.
func bounceBar(width, frame int) string {
	const block = 3
	span := width - block
	if span <= 0 {
		return strings.Repeat("▓", width)
	}
	pos := frame % (2 * span)
	if pos > span {
		pos = 2*span - pos
	}
	return strings.Repeat("░", pos) + strings.Repeat("▓", block) + strings.Repeat("░", width-block-pos)
}

// sparkline draws speed samples scaled between their own low and high,
// so ups and downs show at any speed. A steady rate (within 10%) draws
// a flat mid-height line rather than a wall of full blocks.
func sparkline(samples []float64, width int) string {
	if len(samples) == 0 || width <= 0 {
		return ""
	}
	if len(samples) > width {
		samples = samples[len(samples)-width:]
	}
	lo, hi := samples[0], samples[0]
	for _, s := range samples {
		lo, hi = min(lo, s), max(hi, s)
	}
	top := len(sparkLevels) - 1
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", width-len(samples)))
	for _, s := range samples {
		lvl := top / 2
		if hi > 0 && hi-lo > hi*0.1 {
			lvl = int(math.Round((s - lo) / (hi - lo) * float64(top)))
		}
		b.WriteString(sparkLevels[min(max(lvl, 0), top)])
	}
	return b.String()
}
