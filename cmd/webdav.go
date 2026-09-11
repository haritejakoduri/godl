package cmd

import (
	"github.com/spf13/cobra"

	"godl/internal/daemon"
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

		output, err = outputPath(output, "")
		if err != nil {
			return err
		}
		return startJob(daemon.Request{
			Cmd:       daemon.CmdAddWebDAV,
			Source:    daemon.JoinWebDAVSource(connName, remotePath),
			Output:    output,
			LimitRate: limitRate,
		})
	},
}

func init() {
	webdavCmd.Flags().StringP("output", "o", "", "output directory (default: your Downloads folder)")
	webdavCmd.Flags().StringP("limit-rate", "R", "", "cap this download's speed, e.g. 500K or 2M (default: unlimited)")
}
