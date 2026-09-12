package tray

import (
	"godl/internal/daemon"
	"godl/internal/store"
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
