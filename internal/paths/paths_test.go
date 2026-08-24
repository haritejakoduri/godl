package paths

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDownloadsDirDefaultsUnderHome(t *testing.T) {
	t.Setenv("GODL_DOWNLOADS_DIR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := DownloadsDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Downloads")
	if got != want {
		t.Errorf("DownloadsDir() = %q, want %q", got, want)
	}
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Errorf("DownloadsDir() did not create %q: %v", got, err)
	}
}

func TestDownloadsDirHonorsOverride(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "custom-downloads")
	t.Setenv("GODL_DOWNLOADS_DIR", dir)

	got, err := DownloadsDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Errorf("DownloadsDir() = %q, want %q", got, dir)
	}
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Errorf("DownloadsDir() did not create the overridden dir %q: %v", got, err)
	}
}
