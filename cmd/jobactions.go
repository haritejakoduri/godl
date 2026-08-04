package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
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
			fmt.Printf("%s %s (%s, %s/%s)\n", j.ID, verb, j.Status, humanBytes(j.BytesDone), humanBytes(j.BytesTotal))
			return nil
		},
	}
}

var pauseCmd = jobActionCmd("pause <job-id>", "Pause a running job", daemon.CmdPause, "paused")
var resumeCmd = jobActionCmd("resume <job-id>", "Resume a paused (or failed) job from where it left off", daemon.CmdResume, "resumed")
var retryCmd = jobActionCmd("retry <job-id>", "Reinitiate a job from scratch", daemon.CmdRetry, "retried")
var cancelCmd = jobActionCmd("cancel <job-id>", "Cancel a job", daemon.CmdCancel, "canceled")
