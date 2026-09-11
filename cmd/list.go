package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
	"godl/internal/format"
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
				progress = fmt.Sprintf("%.0f%% (%s/%s)", format.Percent(j.BytesDone, j.BytesTotal)*100, format.Bytes(j.BytesDone), format.Bytes(j.BytesTotal))
			} else if j.BytesDone > 0 {
				progress = format.Bytes(j.BytesDone)
			}
			rows = append(rows, fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s",
				j.ID, j.Type, statusCell(j.Status, j.ErrorMsg), progress, format.Speed(j.SpeedBps), format.Truncate(j.Source, 50)))
		}
		return printTable("ID\tTYPE\tSTATUS\tPROGRESS\tSPEED\tSOURCE", rows)
	},
}
