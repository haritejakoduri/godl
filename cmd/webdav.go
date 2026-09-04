package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
	"godl/internal/paths"
	"godl/internal/store"
)

var webdavCmd = &cobra.Command{
	Use:   "webdav <connection> <remote-path>",
	Short: "Download a file, or an entire folder recursively, from a saved WebDAV connection",
	Long: `Downloads from a WebDAV server using credentials saved with
"godl connection add". If <remote-path> is a file, just that file is
downloaded; if it's a folder, the whole folder is downloaded
recursively, preserving its directory structure under -o.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		connName, remotePath := args[0], args[1]
		output, _ := cmd.Flags().GetString("output")
		limitRate, err := limitRateFlag(cmd)
		if err != nil {
			return err
		}

		if output == "" {
			def, err := paths.DownloadsDir()
			if err != nil {
				return err
			}
			output = def
		}
		abs, err := resolveOutputPath(output)
		if err != nil {
			return err
		}
		output = abs

		if err := daemon.EnsureRunning(); err != nil {
			return err
		}
		resp, err := daemon.Call(daemon.Request{
			Cmd:       daemon.CmdAddWebDAV,
			Source:    daemon.JoinWebDAVSource(connName, remotePath),
			Output:    output,
			LimitRate: limitRate,
		})
		if err != nil {
			return err
		}
		if resp.Job.Status == store.StatusFailed {
			return fmt.Errorf("job %s failed immediately: %s", resp.Job.ID, resp.Job.ErrorMsg)
		}
		fmt.Printf("Started job %s -> %s\n", resp.Job.ID, output)
		fmt.Println(`Track it with "godl status" or "godl list".`)
		return nil
	},
}

func init() {
	webdavCmd.Flags().StringP("output", "o", "", "output directory (default: your Downloads folder)")
	webdavCmd.Flags().StringP("limit-rate", "R", "", "cap this download's speed, e.g. 500K or 2M (default: unlimited)")
}
