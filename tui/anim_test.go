package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"

	"godl/internal/daemon"
	"godl/internal/store"
)

// Every frame of every bar must be exactly barWidth cells: the table
// pads by width, so a bar that grows or shrinks by a cell mid-animation
// would make the whole row jitter.
func TestRenderBarKeepsItsWidthOnEveryFrame(t *testing.T) {
	for _, status := range []store.JobStatus{store.StatusActive, store.StatusPaused, store.StatusCompleted} {
		for _, known := range []bool{true, false} {
			for frame := 0; frame < 60; frame++ {
				for _, frac := range []float64{0, 0.013, 0.5, 0.937, 1, 1.4, -0.2} {
					bar := renderBar(frac, barWidth, frame, status, known)
					if w := runewidth.StringWidth(bar); w != barWidth {
						t.Fatalf("renderBar(%v, frame %d, %s, known=%v) = %q, width %d, want %d", frac, frame, status, known, bar, w, barWidth)
					}
				}
			}
		}
	}
}

// Eighth-block precision: a bar a sixteenth of a cell further along must
// look different, so a slow download still visibly creeps forward.
func TestRenderBarShowsSubCellProgress(t *testing.T) {
	a := renderBar(0.50, barWidth, 0, store.StatusPaused, true)
	b := renderBar(0.50+1.0/(barWidth*8), barWidth, 0, store.StatusPaused, true)
	if a == b {
		t.Errorf("an eighth of a cell more progress rendered identically: %q", a)
	}
	if full := renderBar(1, barWidth, 0, store.StatusCompleted, true); full != strings.Repeat("█", barWidth) {
		t.Errorf("a finished bar = %q, want all full blocks", full)
	}
}

func TestBounceBarMovesAndStaysInBounds(t *testing.T) {
	seen := map[string]bool{}
	for frame := 0; frame < 40; frame++ {
		b := bounceBar(barWidth, frame)
		if strings.Count(b, "▓") != 3 {
			t.Fatalf("frame %d: %q lost its block", frame, b)
		}
		seen[b] = true
	}
	if len(seen) < barWidth-3 {
		t.Errorf("the indeterminate bar only took %d positions", len(seen))
	}
}

func TestSparklineScalesBetweenLowAndHigh(t *testing.T) {
	if got := sparkline([]float64{200, 250, 300}, 5); got != "  ▁▅█" {
		t.Errorf("sparkline = %q, want %q", got, "  ▁▅█")
	}
	if got := sparkline([]float64{100, 104, 98}, 3); got != "▄▄▄" {
		t.Errorf("a steady rate drew %q, want a flat %q", got, "▄▄▄")
	}
	if got := sparkline(nil, 5); got != "" {
		t.Errorf("sparkline(nil) = %q, want empty", got)
	}
	long := make([]float64, 20)
	if w := runewidth.StringWidth(sparkline(long, 6)); w != 6 {
		t.Errorf("sparkline of 20 samples is %d wide, want clipped to 6", w)
	}
}

func testAnim() *animState {
	a := newAnimState()
	a.off = false
	return a
}

func progressJob(id string, done, total int64, status store.JobStatus) *daemon.JobView {
	return &daemon.JobView{Job: &store.Job{ID: id, Status: status, BytesDone: done, BytesTotal: total}}
}

// A bar eases toward a new snapshot's value over several frames rather
// than jumping, never overshoots, and then lets the frame loop stop.
func TestBarsEaseToTheirTargetThenSettle(t *testing.T) {
	a := testAnim()
	jobs := []*daemon.JobView{progressJob("j", 0, 100, store.StatusPaused)}
	a.observe(jobs)
	jobs[0].BytesDone = 80
	a.observe(jobs)

	prev := a.shownFraction(jobs[0])
	if prev != 0 {
		t.Fatalf("bar jumped straight to %v before any frame", prev)
	}
	for i := 0; i < animFPS*3; i++ {
		a.step(jobs)
		cur := a.shownFraction(jobs[0])
		if cur < prev || cur > 0.8 {
			t.Fatalf("frame %d: bar went %v -> %v (target 0.8)", i, prev, cur)
		}
		prev = cur
	}
	if prev != 0.8 {
		t.Errorf("bar settled at %v, want 0.8", prev)
	}
	if a.needsFrames(jobs) {
		t.Error("a settled, paused job still wants frames — an idle dashboard would keep redrawing")
	}
}

func TestFirstSightDoesNotReplayProgress(t *testing.T) {
	a := testAnim()
	j := progressJob("j", 70, 100, store.StatusPaused)
	a.observe([]*daemon.JobView{j})
	if got := a.shownFraction(j); got != 0.7 {
		t.Errorf("a job seen for the first time shows %v, want its real 0.7", got)
	}
}

func TestCompletionFlashesThenStops(t *testing.T) {
	a := testAnim()
	now := time.Unix(1000, 0)
	a.now = func() time.Time { return now }
	j := progressJob("j", 90, 100, store.StatusActive)
	a.observe([]*daemon.JobView{j})
	j.Status, j.BytesDone = store.StatusCompleted, 100
	a.observe([]*daemon.JobView{j})
	if !a.flashing("j") {
		t.Fatal("a job that just completed isn't flashing")
	}
	now = now.Add(completionFlash + time.Millisecond)
	if a.flashing("j") {
		t.Error("the completion flash never ends")
	}
}

func TestReducedMotionSnapsAndNeverTicks(t *testing.T) {
	a := testAnim()
	a.off = true
	j := progressJob("j", 0, 100, store.StatusActive)
	a.observe([]*daemon.JobView{j})
	j.BytesDone = 50
	a.observe([]*daemon.JobView{j})
	if got := a.shownFraction(j); got != 0.5 {
		t.Errorf("with animation off the bar shows %v, want 0.5 at once", got)
	}
	if cmd := a.kick([]*daemon.JobView{j}); cmd != nil {
		t.Error("with animation off a frame tick was still scheduled")
	}
	if a.frameNo() != 0 {
		t.Error("with animation off the frame counter still advances")
	}
}

// Only one frame tick may be in flight: a second snapshot arriving while
// frames are already running must not start another loop.
func TestKickStartsOnlyOneFrameLoop(t *testing.T) {
	a := testAnim()
	jobs := []*daemon.JobView{progressJob("j", 10, 100, store.StatusActive)}
	a.observe(jobs)
	if a.kick(jobs) == nil {
		t.Fatal("an active job didn't start the frame loop")
	}
	if a.kick(jobs) != nil {
		t.Error("a second kick started a second frame loop")
	}
}

func TestStatusIconsAnimateOnlyWhileRunning(t *testing.T) {
	if statusIcon(store.StatusActive, 0, false) == statusIcon(store.StatusActive, 1, false) {
		t.Error("the active spinner doesn't move between frames")
	}
	if statusIcon(store.StatusCompleted, 0, false) != statusIcon(store.StatusCompleted, 7, false) {
		t.Error("a settled completed job's icon changes between frames")
	}
	for _, s := range []store.JobStatus{store.StatusQueued, store.StatusActive, store.StatusPaused, store.StatusCompleted, store.StatusSeeding, store.StatusFailed, store.StatusCanceled} {
		for frame := 0; frame < 20; frame++ {
			if w := runewidth.StringWidth(statusIcon(s, frame, true)); w != 1 {
				t.Errorf("statusIcon(%s, %d) is %d cells wide, want 1", s, frame, w)
			}
		}
	}
}
