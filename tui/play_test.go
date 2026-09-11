package tui

import (
	"testing"

	"godl/internal/daemon"
	"godl/internal/store"
)

func TestLocalPlaybackTarget(t *testing.T) {
	cases := []struct {
		name       string
		job        *store.Job
		wantTarget string
		wantOK     bool
	}{
		{
			name:   "not completed yet has no local target regardless of type",
			job:    &store.Job{Type: store.JobURL, Status: store.StatusActive, Output: "/home/alice/Downloads/movie.mkv"},
			wantOK: false,
		},
		{
			name:       "completed url job uses Output directly",
			job:        &store.Job{Type: store.JobURL, Status: store.StatusCompleted, Output: "/home/alice/Downloads/movie.mkv"},
			wantTarget: "/home/alice/Downloads/movie.mkv",
			wantOK:     true,
		},
		{
			name:   "completed url job with no Output has nothing local to play",
			job:    &store.Job{Type: store.JobURL, Status: store.StatusCompleted},
			wantOK: false,
		},
		{
			name:       "completed torrent job joins Output with the recorded content name",
			job:        &store.Job{Type: store.JobTorrent, Status: store.StatusCompleted, Output: "/home/alice/Downloads", ResolvedPaths: []string{"My Torrent Content"}},
			wantTarget: "/home/alice/Downloads/My Torrent Content",
			wantOK:     true,
		},
		{
			name:   "completed torrent job with no resolved content has nothing local to play",
			job:    &store.Job{Type: store.JobTorrent, Status: store.StatusCompleted, Output: "/home/alice/Downloads"},
			wantOK: false,
		},
		{
			name:       "completed social job uses the first resolved path",
			job:        &store.Job{Type: store.JobSocial, Status: store.StatusCompleted, ResolvedPaths: []string{"/home/alice/Downloads/video.mp4"}},
			wantTarget: "/home/alice/Downloads/video.mp4",
			wantOK:     true,
		},
		{
			name:       "completed webdav job uses the first resolved path",
			job:        &store.Job{Type: store.JobWebDAV, Status: store.StatusCompleted, ResolvedPaths: []string{"/home/alice/Downloads/a.mp4", "/home/alice/Downloads/b.mp4"}},
			wantTarget: "/home/alice/Downloads/a.mp4",
			wantOK:     true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			target, ok := localPlaybackTarget(c.job)
			if ok != c.wantOK {
				t.Fatalf("localPlaybackTarget(%+v) ok = %v, want %v", c.job, ok, c.wantOK)
			}
			if ok && target != c.wantTarget {
				t.Errorf("localPlaybackTarget(%+v) target = %q, want %q", c.job, target, c.wantTarget)
			}
		})
	}
}

// TestDoPlayPrefersLocalFileOverStreamingForCompletedJobs is the
// regression test for the actual "unable to open the player" bug:
// pressing o on a completed url/social job used to always re-stream
// the original source URL, which can go stale (an expired signed
// link, e.g.) well before playback is ever requested — while the file
// already on disk is guaranteed to exist. PATH is emptied so mpv.Play
// itself fails predictably (no player installed in this sandbox); what
// matters here is which target it was even asked to play.
func TestDoPlayPrefersLocalFileOverStreamingForCompletedJobs(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	j := &daemon.JobView{Job: &store.Job{
		Type: store.JobURL, Status: store.StatusCompleted,
		Source: "https://example.com/f?token=expired-by-now",
		Output: "/home/alice/Downloads/f.bin",
	}}
	msg := doPlay(j)().(playedMsg)
	if msg.target != "/home/alice/Downloads/f.bin" {
		t.Errorf("doPlay on a completed url job targeted %q, want the local file %q (not the source URL)", msg.target, j.Output)
	}
}

// TestDoPlayStreamsSourceForInProgressJobs confirms the streaming path
// (still genuinely useful — a preview while a job is still
// downloading) is untouched for a job that isn't done yet.
func TestDoPlayStreamsSourceForInProgressJobs(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	j := &daemon.JobView{Job: &store.Job{
		Type: store.JobSocial, Status: store.StatusActive,
		Source: "https://example.com/watch?v=xyz",
	}}
	msg := doPlay(j)().(playedMsg)
	if msg.target != j.Source {
		t.Errorf("doPlay on an in-progress social job targeted %q, want the source %q", msg.target, j.Source)
	}
}

func TestDoPlayRejectsInProgressTorrent(t *testing.T) {
	// No PATH manipulation needed: an in-progress torrent must be
	// rejected before doPlay ever tries to find a player at all.
	j := &daemon.JobView{Job: &store.Job{Type: store.JobTorrent, Status: store.StatusActive}}
	msg := doPlay(j)().(playedMsg)
	if msg.err == nil {
		t.Fatal("doPlay on an in-progress torrent job succeeded, want an error")
	}
	if msg.target != "" {
		t.Errorf("doPlay on a rejected in-progress torrent set target = %q, want empty", msg.target)
	}
}

func TestDoPlayRejectsUnsupportedJobType(t *testing.T) {
	j := &daemon.JobView{Job: &store.Job{Type: store.JobType("unknown"), Status: store.StatusCompleted}}
	msg := doPlay(j)().(playedMsg)
	if msg.err == nil {
		t.Fatal("doPlay on an unsupported job type succeeded, want an error")
	}
}
