package daemon

import (
	"context"
	"testing"

	"godl/internal/store"
)

func TestSelectFilesOnAPausedTorrentKeepsTheChoice(t *testing.T) {
	d := newTestDaemon(t)
	ctx := context.Background()
	d.st.CreateJob(ctx, &store.Job{ID: "t1", Type: store.JobTorrent, Source: "magnet:?xt=urn:btih:abc", Output: t.TempDir(), Status: store.StatusPaused})

	job, err := d.selectFiles(ctx, "t1", "1-3,7")
	if err != nil {
		t.Fatal(err)
	}
	if job.Options.TorrentFiles != "1-3,7" || job.Status != store.StatusPaused {
		t.Errorf("job = %+v, want the choice saved and the job still paused", job)
	}
	if job, _ = d.selectFiles(ctx, "t1", ""); job.Options.TorrentFiles != "" {
		t.Error("an empty choice means every file")
	}

	if _, err := d.selectFiles(ctx, "t1", "9-2"); err == nil {
		t.Error("a malformed choice should be refused")
	}
	d.st.CreateJob(ctx, &store.Job{ID: "u1", Type: store.JobURL, Source: "https://x/y", Output: "/tmp/y", Status: store.StatusPaused})
	if _, err := d.selectFiles(ctx, "u1", "1"); err == nil {
		t.Error("choosing files only applies to torrents")
	}
	if _, err := d.selectFiles(ctx, "nope", "1"); err == nil {
		t.Error("unknown job: want an error")
	}
}
