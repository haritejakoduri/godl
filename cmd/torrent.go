package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
	"godl/internal/paths"
	"godl/internal/store"
)

var torrentCmd = &cobra.Command{
	Use:   "torrent <magnet-or-file>",
	Short: "Download via BitTorrent, from a magnet link or a .torrent file",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		source := args[0]
		output, _ := cmd.Flags().GetString("output")

		if output == "" {
			def, err := paths.TorrentDataDir()
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

		if !strings.HasPrefix(source, "magnet:") {
			absSrc, err := filepath.Abs(source)
			if err != nil {
				return err
			}
			source = absSrc
		}

		if err := daemon.EnsureRunning(); err != nil {
			return err
		}
		resp, err := daemon.Call(daemon.Request{Cmd: daemon.CmdAddTorrent, Source: source, Output: output})
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
	torrentCmd.Flags().StringP("output", "o", "", "output directory (default: godl's data dir under torrents/)")
}
