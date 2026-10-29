package progress

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBar(t *testing.T) {
	for _, tc := range []struct {
		name            string
		done, total     int64
		width           int
		wantFull        int
		wantTotalGlyphs int
	}{
		{"empty", 0, 100, 10, 0, 10},
		{"half", 50, 100, 10, 5, 10},
		{"full", 100, 100, 10, 10, 10},
		// A server that understates Content-Length must not push the
		// bar past its own end and wreck the line it sits in.
		{"over-reported", 150, 100, 10, 10, 10},
		{"unknown total", 50, 0, 10, 0, 10},
		{"negative done", -5, 100, 10, 0, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Bar(tc.done, tc.total, tc.width)
			if !strings.HasPrefix(got, "[") || !strings.HasSuffix(got, "]") {
				t.Fatalf("Bar() = %q, want it bracketed", got)
			}
			inner := []rune(strings.TrimSuffix(strings.TrimPrefix(got, "["), "]"))
			if len(inner) != tc.wantTotalGlyphs {
				t.Errorf("Bar() = %q: %d glyphs, want %d", got, len(inner), tc.wantTotalGlyphs)
			}
			full := 0
			for _, r := range inner {
				if r == '█' {
					full++
				}
			}
			if full != tc.wantFull {
				t.Errorf("Bar() = %q: %d filled, want %d", got, full, tc.wantFull)
			}
		})
	}
	// A width too small for even the brackets must not panic or
	// produce a negative repeat count.
	if got := Bar(1, 2, 1); got != "" {
		t.Errorf("Bar(width 1) = %q, want empty", got)
	}
}

// TestLineWithoutATotal: when the server sends no Content-Length there
// is no denominator, and a bar drawn anyway would be a guess presented
// as a measurement.
func TestLineWithoutATotal(t *testing.T) {
	got := line("ffmpeg", 1024*1024, 0, 1)
	if strings.Contains(got, "[") || strings.Contains(got, "%") {
		t.Errorf("line() = %q, want no bar and no percentage when the total is unknown", got)
	}
	if !strings.Contains(got, "ffmpeg") || !strings.Contains(got, "MiB") {
		t.Errorf("line() = %q, want it to name the transfer and what has arrived", got)
	}
}

func TestLineWithATotal(t *testing.T) {
	got := line("ffmpeg", 50, 100, 1)
	for _, want := range []string{"ffmpeg", "[", "50%", "/"} {
		if !strings.Contains(got, want) {
			t.Errorf("line() = %q, missing %q", got, want)
		}
	}
}

// TestReportThrottles is the point of the throttle: a download calls
// back every few kilobytes, and forwarding all of them would scroll a
// terminal off its feet and bloat a job log.
func TestReportThrottles(t *testing.T) {
	var mu sync.Mutex
	var got []string
	r := New("thing", func(s string) { mu.Lock(); got = append(got, s); mu.Unlock() })

	for i := 0; i < 500; i++ {
		r.Report(int64(i), 1000)
	}
	mu.Lock()
	n := len(got)
	mu.Unlock()
	if n != 1 {
		t.Errorf("500 rapid reports produced %d lines, want 1 (the first); the rest are inside the throttle window", n)
	}
}

// ...but the end is never dropped, or a transfer appears to stop short
// of where it actually got to.
func TestReportAlwaysSendsTheLast(t *testing.T) {
	var got []string
	r := New("thing", func(s string) { got = append(got, s) })

	r.Report(1, 100)   // first, always sent
	r.Report(50, 100)  // throttled away
	r.Report(100, 100) // complete: sent regardless
	if len(got) != 2 {
		t.Fatalf("got %d lines (%q), want 2: the first and the completion", len(got), got)
	}
	if !strings.Contains(got[1], "100%") {
		t.Errorf("last line = %q, want it to show 100%%", got[1])
	}
}

func TestReportAfterTheWindow(t *testing.T) {
	var got []string
	r := New("thing", func(s string) { got = append(got, s) })
	r.Report(1, 100)
	time.Sleep(interval + 50*time.Millisecond)
	r.Report(2, 100)
	if len(got) != 2 {
		t.Errorf("got %d lines, want 2 once the window has passed", len(got))
	}
}

// A nil sink is the daemon's and the player's case; it must not panic
// and Func must report that there is nothing to call.
func TestNilSinkIsInert(t *testing.T) {
	r := New("thing", nil)
	r.Report(1, 2)
	if r.Func() != nil {
		t.Error("Func() returned a callback for a nil sink; the downloader would count bytes for nobody")
	}
	var nilReporter *Reporter
	nilReporter.Report(1, 2) // must not panic
}
