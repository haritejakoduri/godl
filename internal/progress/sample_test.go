package progress

import "testing"

// TestSampleRendering prints what a caller actually sees. Not an
// assertion — a way to look at the thing without a 200MB download.
func TestSampleRendering(t *testing.T) {
	const total = 205 * 1024 * 1024
	for _, done := range []int64{0, total / 8, total / 3, total / 2, total * 7 / 8, total} {
		t.Logf("%s", line("downloading ffmpeg", done, total, 9))
	}
	t.Logf("%s", line("downloading ffmpeg", 12*1024*1024, 0, 3)) // no Content-Length
}
