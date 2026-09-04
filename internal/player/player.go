// Package player launches an external media player — mpv
// (https://mpv.io) or VLC (https://www.videolan.org/vlc) — to stream
// or play a URL or local file, for godl's "o" (open/play) action on a
// job or a WebDAV browse entry.
//
// Unlike internal/ytdlp and internal/ffmpeg, this package never
// auto-downloads either player: those two have a single trusted
// source (a GitHub release, its digest verified against what GitHub
// itself computed) to auto-install from. Neither mpv's nor VLC's
// Windows builds come from a source with that same guarantee, so godl
// only ever looks for one the user already installed themselves —
// PATH-only, with clear install guidance when neither is found.
//
// mpv is tried first (it's long been able to resolve yt-dlp-supported
// URLs itself via a bundled hook, so it needs no help from godl for
// Social links); VLC is the fallback when mpv isn't installed but VLC
// is.
package player

import (
	"encoding/base64"
	"fmt"
	"os/exec"
	"runtime"
)

// Auth is HTTP Basic Auth credentials for a target that needs them
// (WebDAV). Each backend formats it its own way — mpv only accepts a
// pre-built Authorization header value, while VLC has dedicated
// username/password flags — so it's kept structured here rather than
// pre-built into one shape that would fit neither cleanly.
type Auth struct {
	Username, Password string
}

func (a *Auth) empty() bool {
	return a == nil || (a.Username == "" && a.Password == "")
}

type backend struct {
	name, binary string
	args         func(target string, auth *Auth) []string
}

// backends is tried in order: mpv first (today's default, and the one
// that needs no yt-dlp help for Social links), VLC as the fallback.
var backends = []backend{
	{name: "mpv", binary: "mpv", args: mpvArgs},
	{name: "vlc", binary: "vlc", args: vlcArgs},
}

// installHints names the OS-appropriate way to install each supported
// player, for Find's error message when neither is found.
func installHints() string {
	switch runtime.GOOS {
	case "windows":
		return "winget install mpv-player.mpv-CI.MSVC (mpv), or winget install VideoLAN.VLC (VLC)"
	case "darwin":
		return "brew install mpv, or brew install --cask vlc"
	default:
		return "your distro's package manager, e.g. apt install mpv (or vlc)"
	}
}

// Find returns the name and path of the first supported player found
// on PATH (mpv, then VLC), or an error with install guidance for both
// if neither is present.
func Find() (name, path string, err error) {
	for _, b := range backends {
		if p, err := exec.LookPath(b.binary); err == nil {
			return b.name, p, nil
		}
	}
	return "", "", fmt.Errorf("no media player found on PATH (looked for mpv, vlc) — install one yourself (%s), or see https://mpv.io/installation/ / https://www.videolan.org/vlc/", installHints())
}

// Play finds the first available player and launches it on target —
// for callers that don't care which backend ends up running (url,
// webdav, and completed-torrent playback all work identically either
// way). Social jobs care which backend is used (see cmd/status.go's
// doPlay) and should call Find then PlayWith directly instead.
func Play(target string, auth *Auth) error {
	name, _, err := Find()
	if err != nil {
		return err
	}
	return PlayWith(name, target, auth)
}

// PlayWith launches the named backend ("mpv" or "vlc") on target — a
// URL or a local file path, which both players treat identically —
// detached from godl's own process so the TUI doesn't block while it
// plays and closing the TUI doesn't kill it.
func PlayWith(name, target string, auth *Auth) error {
	var b *backend
	for i := range backends {
		if backends[i].name == name {
			b = &backends[i]
			break
		}
	}
	if b == nil {
		return fmt.Errorf("unknown player %q", name)
	}
	path, err := exec.LookPath(b.binary)
	if err != nil {
		return fmt.Errorf("%s not found on PATH: %w", name, err)
	}

	cmd := exec.Command(path, b.args(target, auth)...)
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", name, err)
	}
	// Detached on purpose (see package doc): release rather than Wait,
	// so godl's own process exiting doesn't reap/signal the player.
	return cmd.Process.Release()
}

// mpvArgs passes auth (if any) via mpv's --http-header-fields flag
// rather than embedding it in the URL as user:pass@host: an
// argv-embedded password is readable by any other local user via
// ps/Task Manager, while a header flag isn't meaningfully more
// exposed than the URL/target argument itself already is.
func mpvArgs(target string, auth *Auth) []string {
	if auth.empty() {
		return []string{target}
	}
	token := base64.StdEncoding.EncodeToString([]byte(auth.Username + ":" + auth.Password))
	return []string{"--http-header-fields=Authorization: Basic " + token, target}
}

// vlcArgs uses VLC's own dedicated Basic Auth flags rather than
// building a header string by hand.
func vlcArgs(target string, auth *Auth) []string {
	if auth.empty() {
		return []string{target}
	}
	return []string{"--http-user=" + auth.Username, "--http-pwd=" + auth.Password, target}
}
