// Package mpv launches mpv (https://mpv.io), or VLC as a fallback, to
// stream or play a URL or local file, for godl's "o" (open/play)
// action on a job or a WebDAV browse entry.
//
// Unlike internal/ytdlp and internal/ffmpeg, this package never
// auto-downloads either player: those two have a single trusted source
// (a GitHub release, its digest verified against what GitHub itself
// computed) to auto-install from. mpv's Windows builds come from an
// unofficial community project with no equivalent guarantee, and VLC
// isn't distributed as a plain downloadable binary at all — so godl
// only ever looks for a player the user already installed themselves,
// with clear install guidance when neither is found.
package mpv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// installHint names the OS-appropriate way to install mpv, for an
// error message when neither player is found.
func installHint() string {
	switch runtime.GOOS {
	case "windows":
		return "winget install mpv-player.mpv-CI.MSVC"
	case "darwin":
		return "brew install mpv"
	default:
		return "your distro's package manager, e.g. apt install mpv / dnf install mpv / pacman -S mpv"
	}
}

// Path returns the path to an mpv binary already on PATH, or an error
// with install guidance if none is found.
func Path() (string, error) {
	p, err := exec.LookPath("mpv")
	if err != nil {
		return "", fmt.Errorf("mpv not found on PATH — install it yourself (%s), or see https://mpv.io/installation/", installHint())
	}
	return p, nil
}

// vlcCandidatePaths returns the standard per-machine install locations
// VLC's own Windows installer places vlc.exe at, checked because —
// unlike mpv, which is mostly used by people who've deliberately put
// it on PATH — VLC's Windows installer doesn't reliably add itself to
// PATH, so exec.LookPath alone would miss a perfectly normal install.
// goos/getenv are parameters (rather than reading runtime.GOOS/
// os.Getenv directly) purely so a test can exercise this on any
// platform instead of only whichever one happens to run the suite.
func vlcCandidatePaths(goos string, getenv func(string) string) []string {
	if goos != "windows" {
		return nil
	}
	var paths []string
	for _, envVar := range []string{"ProgramFiles", "ProgramFiles(x86)"} {
		if dir := getenv(envVar); dir != "" {
			// A literal backslash join, not filepath.Join: this
			// function is parameterized on goos specifically so tests
			// can exercise the Windows path on any host platform, but
			// filepath.Join always uses the *building* platform's own
			// separator regardless of goos — on a non-Windows test
			// runner that would silently produce forward-slash paths
			// for a codepath that only ever really executes on
			// Windows, where they must be backslashed.
			paths = append(paths, dir+`\VideoLAN\VLC\vlc.exe`)
		}
	}
	return paths
}

// vlcPath returns the path to a VLC binary — on PATH, or (Windows
// only) one of vlcCandidatePaths' standard install locations.
func vlcPath() (string, error) {
	if p, err := exec.LookPath("vlc"); err == nil {
		return p, nil
	}
	for _, p := range vlcCandidatePaths(runtime.GOOS, os.Getenv) {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("vlc not found")
}

// playerKind distinguishes mpv from VLC only where their command-line
// interfaces actually differ — right now, just custom HTTP headers
// (see findPlayer/Play).
type playerKind int

const (
	playerMPV playerKind = iota
	playerVLC
)

// findPlayer locates a player to hand a target to, preferring mpv
// (lighter, and the one whose flags this package knows how to speak
// fully — see Play's header handling) but falling back to VLC when mpv
// isn't available. VLC is by far the more commonly already-installed
// player, especially on Windows.
func findPlayer() (path string, kind playerKind, err error) {
	if p, err := Path(); err == nil {
		return p, playerMPV, nil
	}
	if p, err := vlcPath(); err == nil {
		return p, playerVLC, nil
	}
	return "", 0, fmt.Errorf("no media player found — install mpv (%s) or VLC (https://www.videolan.org/vlc/) to use this", installHint())
}

// Play launches a player on target — a URL or a local file path,
// which both mpv and VLC treat identically — detached from godl's own
// process so the TUI doesn't block while it plays and closing the TUI
// doesn't kill it.
//
// headers, if non-nil, are passed via mpv's --http-header-fields flag
// (e.g. {"Authorization": "Basic ..."}) rather than embedded in the
// URL as user:pass@host: an argv-embedded password is readable by any
// other local user via ps/Task Manager, while a header flag isn't
// meaningfully more exposed than the URL/target argument itself
// already is. VLC has no equivalent flag for arbitrary headers, so a
// target that needs them is refused when only VLC is available rather
// than falling back to the less-safe URL-embedded form.
func Play(target string, headers map[string]string) error {
	playerPath, kind, err := findPlayer()
	if err != nil {
		return err
	}
	if kind == playerVLC && len(headers) > 0 {
		return fmt.Errorf("VLC can't send this job's required request headers — install mpv instead (%s), or see https://mpv.io/installation/", installHint())
	}

	var args []string
	if len(headers) > 0 {
		fields := make([]string, 0, len(headers))
		for k, v := range headers {
			fields = append(fields, fmt.Sprintf("%s: %s", k, v))
		}
		args = append(args, "--http-header-fields="+strings.Join(fields, ","))
	}
	args = append(args, target)

	cmd := exec.Command(playerPath, args...)
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting %s: %w", filepath.Base(playerPath), err)
	}
	// Detached on purpose (see doc comment): release rather than Wait,
	// so godl's own process exiting doesn't reap/signal the player.
	return cmd.Process.Release()
}
