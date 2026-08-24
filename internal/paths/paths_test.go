package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDownloadsDirDefaultsUnderHome(t *testing.T) {
	t.Setenv("GODL_DOWNLOADS_DIR", "")
	t.Setenv("XDG_DOWNLOAD_DIR", "") // don't let the real session's value leak in
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

// The rest are regression tests for the actual bug report: DownloadsDir
// used to always guess ~/Downloads, which is wrong on a Linux desktop
// where the real Downloads folder has been relocated or renamed via
// the standard XDG user-dirs mechanism — godl would then silently
// create and use a second, disconnected "Downloads" folder instead of
// the user's real one. systemDownloadsDir's XDG resolution only exists
// in downloads_linux.go, so these only mean anything on Linux.

func TestDownloadsDirHonorsXDGDownloadDirEnvVar(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG user-dirs resolution is Linux-specific")
	}
	t.Setenv("GODL_DOWNLOADS_DIR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	relocated := filepath.Join(t.TempDir(), "relocated-downloads")
	t.Setenv("XDG_DOWNLOAD_DIR", relocated)

	got, err := DownloadsDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != relocated {
		t.Errorf("DownloadsDir() = %q, want the XDG_DOWNLOAD_DIR value %q, not a guessed ~/Downloads", got, relocated)
	}
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Errorf("DownloadsDir() did not create %q: %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(home, "Downloads")); err == nil {
		t.Error("DownloadsDir() should not have also created a ~/Downloads it isn't using")
	}
}

func TestDownloadsDirHonorsUserDirsDotDirsFile(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG user-dirs resolution is Linux-specific")
	}
	t.Setenv("GODL_DOWNLOADS_DIR", "")
	t.Setenv("XDG_DOWNLOAD_DIR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	dirsFile := "# comment line, should be ignored\n" +
		"XDG_DESKTOP_DIR=\"$HOME/Desktop\"\n" +
		"XDG_DOWNLOAD_DIR=\"$HOME/Téléchargements\"\n"
	if err := os.WriteFile(filepath.Join(home, ".config", "user-dirs.dirs"), []byte(dirsFile), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := DownloadsDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Téléchargements")
	if got != want {
		t.Errorf("DownloadsDir() = %q, want the user-dirs.dirs value %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(home, "Downloads")); err == nil {
		t.Error("DownloadsDir() should not have also created a ~/Downloads it isn't using")
	}
}

func TestDownloadsDirFallsBackWhenUserDirsFileHasNoDownloadEntry(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG user-dirs resolution is Linux-specific")
	}
	t.Setenv("GODL_DOWNLOADS_DIR", "")
	t.Setenv("XDG_DOWNLOAD_DIR", "")
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := os.MkdirAll(filepath.Join(home, ".config"), 0o755); err != nil {
		t.Fatal(err)
	}
	dirsFile := `XDG_DESKTOP_DIR="$HOME/Desktop"` + "\n"
	if err := os.WriteFile(filepath.Join(home, ".config", "user-dirs.dirs"), []byte(dirsFile), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := DownloadsDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Downloads")
	if got != want {
		t.Errorf("DownloadsDir() = %q, want the ~/Downloads fallback %q", got, want)
	}
}
