package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
	"godl/internal/format"
	"godl/internal/store"
)

func jobActionCmd(use, short, apiCmd, verb string) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := daemon.EnsureRunning(); err != nil {
				return err
			}
			resp, err := daemon.Call(daemon.Request{Cmd: apiCmd, JobID: args[0]})
			if err != nil {
				return err
			}
			j := resp.Job
			// Pausing or canceling a seeding torrent only stops the
			// upload; the download itself stays completed.
			if (apiCmd == daemon.CmdPause || apiCmd == daemon.CmdCancel) && j.Status == store.StatusCompleted {
				verb = "stopped seeding"
			}
			fmt.Printf("%s %s (%s, %s/%s)\n", j.ID, verb, j.Status, format.Bytes(j.BytesDone), format.Bytes(j.BytesTotal))
			return nil
		},
	}
}

var pauseCmd = jobActionCmd("pause <job-id>", "Pause a running job", daemon.CmdPause, "paused")
var resumeCmd = jobActionCmd("resume <job-id>", "Resume a paused (or failed) job from where it left off", daemon.CmdResume, "resumed")
var retryCmd = jobActionCmd("retry <job-id>", "Reinitiate a job from scratch", daemon.CmdRetry, "retried")
var cancelCmd = jobActionCmd("cancel <job-id>", "Cancel a job", daemon.CmdCancel, "canceled")
