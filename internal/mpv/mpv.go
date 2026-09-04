// Package mpv launches mpv (https://mpv.io) to stream or play a URL or
// local file, for godl's "o" (open/play) action on a job or a WebDAV
// browse entry.
//
// Unlike internal/ytdlp and internal/ffmpeg, this package never
// auto-downloads mpv: those two have a single trusted source (a
// GitHub release, its digest verified against what GitHub itself
// computed) to auto-install from. mpv's Windows builds come from an
// unofficial community project with no equivalent guarantee, so godl
// only ever looks for an mpv the user already installed themselves —
// PATH-only, with clear install guidance when it's missing.
package mpv

import (
	"fmt"
	"os/exec"
	"runtime"
)

// installHint names the OS-appropriate way to install mpv, for Path's
// error message when it isn't found.
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

// Play launches mpv on target — a URL or a local file path, which mpv
// treats identically — detached from godl's own process so the TUI
// doesn't block while it plays and closing the TUI doesn't kill it.
//
// headers, if non-nil, are passed via mpv's --http-header-fields flag
// (e.g. {"Authorization": "Basic ..."}) rather than embedded in the
// URL as user:pass@host: an argv-embedded password is readable by any
// other local user via ps/Task Manager, while a header flag isn't
// meaningfully more exposed than the URL/target argument itself
// already is.
func Play(target string, headers map[string]string) error {
	mpvPath, err := Path()
	if err != nil {
		return err
	}

	args := make([]string, 0, len(headers)+1)
	if len(headers) > 0 {
		fields := make([]string, 0, len(headers))
		for k, v := range headers {
			fields = append(fields, fmt.Sprintf("%s: %s", k, v))
		}
		args = append(args, "--http-header-fields="+joinComma(fields))
	}
	args = append(args, target)

	cmd := exec.Command(mpvPath, args...)
	cmd.SysProcAttr = detachedSysProcAttr()
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting mpv: %w", err)
	}
	// Detached on purpose (see doc comment): release rather than Wait,
	// so godl's own process exiting doesn't reap/signal mpv.
	return cmd.Process.Release()
}

func joinComma(fields []string) string {
	out := fields[0]
	for _, f := range fields[1:] {
		out += "," + f
	}
	return out
}
