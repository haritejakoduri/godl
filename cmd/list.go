package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List all jobs (active/paused/queued/completed/failed)",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := daemon.EnsureRunning(); err != nil {
			return err
		}
		resp, err := daemon.Call(daemon.Request{Cmd: daemon.CmdList})
		if err != nil {
			return err
		}
		if len(resp.Jobs) == 0 {
			fmt.Println("No jobs yet. Try \"godl url\", \"godl social\", or \"godl torrent\".")
			return nil
		}

		rows := make([]string, 0, len(resp.Jobs))
		for _, j := range resp.Jobs {
			progress := "-"
			if j.BytesTotal > 0 {
				progress = fmt.Sprintf("%.0f%% (%s/%s)", percent(j.BytesDone, j.BytesTotal)*100, humanBytes(j.BytesDone), humanBytes(j.BytesTotal))
			} else if j.BytesDone > 0 {
				progress = humanBytes(j.BytesDone)
			}
			rows = append(rows, fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s",
				j.ID, j.Type, statusCell(j.Status, j.ErrorMsg), progress, humanSpeed(j.SpeedBps), truncate(j.Source, 50)))
		}
		return printTable("ID\tTYPE\tSTATUS\tPROGRESS\tSPEED\tSOURCE", rows)
	},
}
