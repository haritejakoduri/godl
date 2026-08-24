//go:build linux

package paths

import (
	"os"
	"path/filepath"
	"strings"
)

// systemDownloadsDir resolves the Linux XDG user-dirs "downloads"
// location, if one is actually configured — the $XDG_DOWNLOAD_DIR env
// var first (some session managers export it directly), then
// ~/.config/user-dirs.dirs (what xdg-user-dirs-update, run by GNOME/
// KDE/etc. on first login, actually writes there). That's the real
// Downloads folder on a Linux desktop: it can be relocated or even
// renamed entirely (a non-English locale names it something else, e.g.
// ~/Téléchargements on a French system), so it isn't reliably
// ~/Downloads. Returns "" if neither source is configured, leaving the
// caller to fall back to that ~/Downloads default instead of guessing
// at a nonstandard format.
func systemDownloadsDir(home string) string {
	if v := os.Getenv("XDG_DOWNLOAD_DIR"); v != "" {
		return expandXDGValue(v, home)
	}
	data, err := os.ReadFile(filepath.Join(home, ".config", "user-dirs.dirs"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		v, ok := strings.CutPrefix(line, "XDG_DOWNLOAD_DIR=")
		if !ok {
			continue
		}
		return expandXDGValue(strings.Trim(v, `"`), home)
	}
	return ""
}

// expandXDGValue expands the one variable user-dirs.dirs values
// actually use ($HOME) and requires the result to be an absolute
// path — the file format allows arbitrary shell-style content we don't
// need to support, just this one substitution.
func expandXDGValue(v, home string) string {
	v = strings.ReplaceAll(v, "$HOME", home)
	if !filepath.IsAbs(v) {
		return ""
	}
	return v
}
