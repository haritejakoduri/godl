package cmd

import (
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
)

var torrentCmd = &cobra.Command{
	Use:   "torrent <magnet-or-file>",
	Short: "Download via BitTorrent, from a magnet link or a .torrent file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		source := args[0]
		output, _ := cmd.Flags().GetString("output")
		limitRate, err := limitRateFlag(cmd)
		if err != nil {
			return err
		}

		output, err = outputPath(output, "")
		if err != nil {
			return err
		}

		if !strings.HasPrefix(source, "magnet:") {
			absSrc, err := filepath.Abs(source)
			if err != nil {
				return err
			}
			source = absSrc
		}

		return startJob(daemon.Request{Cmd: daemon.CmdAddTorrent, Source: source, Output: output, LimitRate: limitRate})
	},
}

func init() {
	torrentCmd.Flags().StringP("output", "o", "", "output directory (default: your Downloads folder)")
	torrentCmd.Flags().StringP("limit-rate", "R", "", "cap download speed, e.g. 500K or 2M (default: unlimited). Shared across all active torrent jobs, not just this one — anacrolix/torrent's rate limiter is client-wide")
}
