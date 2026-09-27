package store

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "godl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestGetSettingsDefaultsWhenNothingSaved(t *testing.T) {
	st := newTestStore(t)
	got, err := st.GetSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := DefaultSettings(); got != want {
		t.Fatalf("GetSettings on a fresh store = %+v, want defaults %+v", got, want)
	}
}

func TestSaveSettingsRoundTrips(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	want := Settings{
		MaxConcurrent:        3,
		DefaultRateLimit:     "2M",
		GlobalRateLimit:      "10M",
		AutoRetry:            true,
		AutoRetryMaxAttempts: 5,
		NotifyOnComplete:     true,
		DownloadDir:          "/data/downloads",
		Connections:          8,
		SocialPreset:         "720p",
		CookiesFromBrowser:   "firefox",
		SeedRatio:            1.5,
		SeedTime:             "2h0m0s",
		NoAnimations:         true,
	}
	if err := st.SaveSettings(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("GetSettings after SaveSettings = %+v, want %+v", got, want)
	}

	// A second save overwrites cleanly rather than appending stray rows
	// (the settings table upserts by key) — round-trip a different value
	// through the same store to catch that regression.
	want2 := Settings{MaxConcurrent: 0, DefaultRateLimit: "", GlobalRateLimit: "", AutoRetry: false, AutoRetryMaxAttempts: 1, NotifyOnComplete: false}
	if err := st.SaveSettings(ctx, want2); err != nil {
		t.Fatal(err)
	}
	got2, err := st.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got2 != want2 {
		t.Fatalf("GetSettings after second SaveSettings = %+v, want %+v", got2, want2)
	}
}

// TestJobRetryCountPersists is the RetryCount analog of the daemon
// package's existing LimitRate/Sha256 persistence regression tests:
// scheduleAutoRetry re-fetches a job from the store before deciding
// whether to re-queue it, so a RetryCount that only lived on the
// in-memory *Job a caller happened to be holding, and never made it
// into the actual row, would silently break auto-retry's attempt cap.
func TestJobRetryCountPersists(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	id, err := st.NewID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	j := &Job{ID: id, Type: JobURL, Source: "http://example.com/f", Output: "/tmp/f", Status: StatusQueued, RetryCount: 2}
	if err := st.CreateJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	reread, err := st.GetJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if reread.RetryCount != 2 {
		t.Fatalf("GetJob after CreateJob: RetryCount = %d, want 2", reread.RetryCount)
	}

	reread.RetryCount = 5
	if err := st.UpdateJob(ctx, reread); err != nil {
		t.Fatal(err)
	}
	afterUpdate, err := st.GetJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if afterUpdate.RetryCount != 5 {
		t.Fatalf("GetJob after UpdateJob: RetryCount = %d, want 5", afterUpdate.RetryCount)
	}
}

func TestJobOptionsRoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	want := JobOptions{
		Headers:      []string{"Referer: https://example.com/"},
		CookiesFile:  "/tmp/cookies.txt",
		TorrentFiles: "1,3-4,*.mkv",
		SeedRatio:    1.5,
		SeedTimeSec:  3600,
	}
	j := &Job{ID: "opts0001", Type: JobTorrent, Source: "magnet:?x", Output: "/tmp", Status: StatusQueued, Options: want}
	if err := st.CreateJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetJob(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Options, want) {
		t.Errorf("Options = %+v, want %+v", got.Options, want)
	}
	if !got.Options.Seeds() {
		t.Error("Seeds() = false with a seed ratio set")
	}
}
