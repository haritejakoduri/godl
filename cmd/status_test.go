package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"godl/internal/daemon"
)

// TestBuildAddRequestDefaultsUnderDownloads is a regression test: url,
// social, and torrent jobs started from the TUI's "n" wizard (like
// their CLI counterparts — see url.go/social.go/torrent.go's RunE)
// must default their Output to the user's actual Downloads folder
// when no explicit path is given, not the current directory or godl's
// own internal data dir.
func TestBuildAddRequestDefaultsUnderDownloads(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GODL_DOWNLOADS_DIR", "")
	t.Setenv("XDG_DOWNLOAD_DIR", "")
	downloads := filepath.Join(home, "Downloads")

	cases := []struct {
		apiCmd string
		source string
	}{
		{daemon.CmdAddURL, "https://example.com/files/report.pdf"},
		{daemon.CmdAddSocial, "https://example.com/watch?v=xyz"},
		{daemon.CmdAddTorrent, "magnet:?xt=urn:btih:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"},
	}
	for _, c := range cases {
		t.Run(c.apiCmd, func(t *testing.T) {
			req, err := buildAddRequest(c.apiCmd, c.source)
			if err != nil {
				t.Fatal(err)
			}
			if req.Output != downloads && !strings.HasPrefix(req.Output, downloads+string(filepath.Separator)) {
				t.Errorf("buildAddRequest(%s) Output = %q, want it under %q", c.apiCmd, req.Output, downloads)
			}
		})
	}
}
