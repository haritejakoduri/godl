package ytdlp

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIsStale(t *testing.T) {
	dir := t.TempDir()

	missing := filepath.Join(dir, "missing")
	if !isStale(missing, time.Hour) {
		t.Error("isStale(missing stamp) = false, want true (never checked counts as stale)")
	}

	fresh := filepath.Join(dir, "fresh")
	if err := os.WriteFile(fresh, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if isStale(fresh, time.Hour) {
		t.Error("isStale(just-written stamp, 1h window) = true, want false")
	}

	old := filepath.Join(dir, "old")
	if err := os.WriteFile(old, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if !isStale(old, time.Hour) {
		t.Error("isStale(2h-old stamp, 1h window) = false, want true")
	}
}
