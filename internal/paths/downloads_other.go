//go:build !windows && !linux

package paths

// systemDownloadsDir has no extra resolution to do on this platform —
// macOS (and other Unix-likes without Linux's XDG user-dirs
// convention) doesn't relocate or rename the Downloads folder the way
// Windows and XDG-configured Linux desktops can, so the "" here just
// tells DownloadsDir to use its ~/Downloads default, which is already
// correct.
func systemDownloadsDir(_ string) string {
	return ""
}
