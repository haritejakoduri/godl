package tray

import (
	"context"
	"fmt"
	"time"

	"godl/internal/appwindow"
	"godl/internal/daemon"
	"godl/internal/notify"
	"godl/internal/paths"
	"godl/internal/selfupdate"
	"godl/internal/store"
	"godl/internal/webui"
)

// bulk applies cmd to every job currently in one of the given statuses.
// Errors on individual jobs are ignored on purpose: a job that finished
// or was removed between listing and acting on it is a race, not a
// failure the user needs told about, and one such job must not stop the
// rest from being paused.
func bulk(cmd string, statuses ...store.JobStatus) error {
	resp, err := daemon.Call(daemon.Request{Cmd: daemon.CmdList})
	if err != nil {
		return err
	}
	want := make(map[store.JobStatus]bool, len(statuses))
	for _, st := range statuses {
		want[st] = true
	}
	for _, j := range resp.Jobs {
		if want[j.Status] {
			daemon.Call(daemon.Request{Cmd: cmd, JobID: j.ID})
		}
	}
	return nil
}

func pauseAll() error {
	return bulk(daemon.CmdPause, store.StatusActive, store.StatusQueued)
}

func resumeAll() error {
	return bulk(daemon.CmdResume, store.StatusPaused)
}

// openWebUI turns the web interface on if it is off, then opens it.
// Mirrors what "godl web" does, because the tray is the one place a
// user who never opens a terminal will reach it from — so it has to
// work from a cold start, not only when the page is already enabled.
func openWebUI() error {
	resp, err := daemon.Call(daemon.Request{Cmd: daemon.CmdGetSettings})
	if err != nil {
		return err
	}
	if resp.Settings == nil {
		return fmt.Errorf("the daemon returned no settings")
	}
	s := *resp.Settings
	if !s.WebUI {
		s.WebUI = true
		if _, err := daemon.Call(daemon.Request{Cmd: daemon.CmdSetSettings, Settings: &s}); err != nil {
			return err
		}
	}

	port := s.WebUIPort
	if port == 0 {
		port = store.DefaultWebUIPort
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/", port)
	if !s.WebUINetwork {
		// Local-only mode authenticates with a token rather than a
		// password, so the link has to carry one or the page refuses us.
		dataDir, err := paths.DataDir()
		if err != nil {
			return err
		}
		tok, err := webui.Token(dataDir)
		if err != nil {
			return err
		}
		url += "?token=" + tok
	}
	return appwindow.Open(url)
}

// restartDaemon restarts the service and says so, since the icon
// disappearing and coming back is otherwise the only feedback and
// looks indistinguishable from a crash.
func restartDaemon() error {
	if err := daemon.Restart(); err != nil {
		notify.Send("godl", "Couldn't restart the daemon: "+err.Error())
		return err
	}
	notify.Send("godl", "Background daemon restarted. Downloads carry on from where they were.")
	return nil
}

// updateTimeout bounds the update check. It downloads a release binary,
// so it is allowed to be slow, but not to hang the menu item forever.
const updateTimeout = 15 * time.Minute

// checkForUpdates runs the same self-update "godl update" does and
// reports the outcome as a desktop notification — a tray has no other
// way to tell the user what happened, and silently swapping the binary
// under them would be worse than saying nothing at all.
func checkForUpdates() error {
	ctx, cancel := context.WithTimeout(context.Background(), updateTimeout)
	defer cancel()

	result, latest, err := selfupdate.ForceUpdate(ctx, func(string) {})
	switch {
	case err != nil:
		notify.Send("godl", "Update check failed: "+err.Error())
		return err
	case result == selfupdate.Updated:
		// The running daemon is still the old binary; restarting is
		// what actually puts the new one in service.
		notify.Send("godl", fmt.Sprintf("Updated to %s. Restarting the daemon…", latest))
		return restartDaemon()
	case result == selfupdate.ManagedInstall:
		notify.Send("godl", fmt.Sprintf("godl %s is available — install it with your package manager.", latest))
	case result == selfupdate.Unsupported:
		notify.Send("godl", "This build can't update itself. Reinstall to upgrade.")
	default:
		notify.Send("godl", "godl is already up to date.")
	}
	return nil
}
