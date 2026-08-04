package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
)

var removeCmd = &cobra.Command{
	Use:     "remove <job-id>",
	Aliases: []string{"rm"},
	Short:   "Remove a job from the list",
	Long: `Remove a job from the list. Stops it first if it's still active/queued.

By default only the list entry goes away — the downloaded (or
partially downloaded) file is left on disk. Pass --purge to also
delete it: for url jobs that's the exact output file; for
torrent/social jobs, godl deletes the specific file(s) it resolved
during the download, not the whole -o directory (which usually holds
other things too).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		purge, _ := cmd.Flags().GetBool("purge")
		if err := daemon.EnsureRunning(); err != nil {
			return err
		}
		resp, err := daemon.Call(daemon.Request{Cmd: daemon.CmdRemove, JobID: args[0], Purge: purge})
		if err != nil {
			return err
		}
		if purge {
			fmt.Printf("Removed %s and deleted its downloaded file(s)\n", resp.Job.ID)
		} else {
			fmt.Printf("Removed %s from the list (files kept)\n", resp.Job.ID)
		}
		return nil
	},
}

func init() {
	removeCmd.Flags().BoolP("purge", "p", false, "also delete the downloaded file(s)")
}
