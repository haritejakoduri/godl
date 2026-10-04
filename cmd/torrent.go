package cmd

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
	"godl/internal/format"
	"godl/internal/store"
	"godl/internal/torrentmgr"
)

var torrentCmd = &cobra.Command{
	Use:   "torrent <magnet-or-file> | -i <links-file>",
	Short: "Download via BitTorrent, from a magnet link or a .torrent file",
	Long: `Download via BitTorrent, from a magnet link or a .torrent file.

Pick only some of a torrent's files — list them first, then pass their
numbers, ranges or glob patterns to --files:

  godl torrent <magnet> --list-files
  godl torrent <magnet> --files 2,5-7
  godl torrent <magnet> --files "*.mkv"

Keep sharing after the download finishes until a ratio or a time limit
is reached, whichever comes first:

  godl torrent <magnet> --seed-ratio 1.5
  godl torrent <magnet> --seed-time 2h

Send it through TorBox (torbox.app) instead: TorBox downloads the
torrent on its servers, then godl fetches the files from TorBox over a
fast direct connection. Set your TorBox API key in Settings ("s" in
"godl status", or the web interface) first:

  godl torrent <magnet> --torbox

With "Use TorBox for new torrents" on, --p2p sends one past TorBox.

Watch it while it downloads with "godl stream <job-id>" (or "o" in
"godl status").`,
	Args: batchArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		all, err := links(cmd, args)
		if err != nil {
			return err
		}
		for i, source := range all {
			if !strings.HasPrefix(source, "magnet:") {
				abs, err := filepath.Abs(source)
				if err != nil {
					return err
				}
				all[i] = abs
			}
		}

		if list, _ := cmd.Flags().GetBool("list-files"); list {
			if len(all) != 1 {
				return fmt.Errorf("--list-files takes exactly one torrent")
			}
			return listTorrentFiles(all[0])
		}

		output, _ := cmd.Flags().GetString("output")
		limitRate, err := limitRateFlag(cmd)
		if err != nil {
			return err
		}
		output, err = outputPath(output, "")
		if err != nil {
			return err
		}

		files, _ := cmd.Flags().GetString("files")
		if _, err := torrentmgr.ParseSelection(files); err != nil {
			return err
		}
		seedRatio, _ := cmd.Flags().GetFloat64("seed-ratio")
		seedTime, _ := cmd.Flags().GetDuration("seed-time")
		if seedRatio < 0 || seedTime < 0 {
			return fmt.Errorf("--seed-ratio and --seed-time can't be negative")
		}

		useTorBox, _ := cmd.Flags().GetBool("torbox")
		useP2P, _ := cmd.Flags().GetBool("p2p")
		via := ""
		switch {
		case useTorBox && useP2P:
			return fmt.Errorf("pick one of --torbox and --p2p")
		case useTorBox:
			via = store.ViaTorBox
		case useP2P:
			via = store.ViaP2P
		}
		if via == store.ViaTorBox && (seedRatio > 0 || seedTime > 0) {
			return fmt.Errorf("a torrent sent through TorBox isn't seeded from here; drop --seed-ratio/--seed-time")
		}

		reqs := make([]daemon.Request, len(all))
		for i, source := range all {
			reqs[i] = daemon.Request{Cmd: daemon.CmdAddTorrent, Source: source, Output: output, LimitRate: limitRate}
			reqs[i].Options.TorrentFiles = files
			reqs[i].Options.SeedRatio = seedRatio
			reqs[i].Options.SeedTimeSec = int64(seedTime.Seconds())
			reqs[i].Options.Via = via
		}
		return startJobs(reqs)
	},
}

// listTorrentFiles prints a torrent's files, numbered the way --files
// takes them. For a magnet link that means fetching the metadata from
// peers first, which the daemon does without downloading any content.
func listTorrentFiles(source string) error {
	if err := daemon.EnsureRunning(); err != nil {
		return err
	}
	if strings.HasPrefix(source, "magnet:") {
		fmt.Println("Fetching the torrent's file list from peers...")
	}
	resp, err := daemon.Call(daemon.Request{Cmd: daemon.CmdTorrentFiles, Source: source})
	if err != nil {
		return err
	}
	fmt.Printf("%s — %d file(s)\n\n", resp.Name, len(resp.Files))
	rows := make([]string, len(resp.Files))
	var total int64
	for i, f := range resp.Files {
		rows[i] = fmt.Sprintf("%d\t%s\t%s", f.Index+1, format.Bytes(f.Length), f.Path)
		total += f.Length
	}
	if err := printTable("#\tSIZE\tPATH", rows); err != nil {
		return err
	}
	fmt.Printf("\nTotal %s. Download only some with --files, e.g. --files 1,3-4 or --files \"*.mkv\".\n", format.Bytes(total))
	return nil
}

func init() {
	torrentCmd.Flags().StringP("output", "o", "", "output directory (default: your Downloads folder)")
	torrentCmd.Flags().StringP("limit-rate", "R", "", "cap download speed, e.g. 500K or 2M (default: unlimited). Shared across all active torrent jobs, not just this one — anacrolix/torrent's rate limiter is client-wide")
	torrentCmd.Flags().Bool("list-files", false, "list the torrent's files (numbered for --files) and exit, without downloading")
	torrentCmd.Flags().String("files", "", `download only these files: numbers and ranges from --list-files and/or glob patterns, e.g. "1,3-5" or "*.mkv" (default: all)`)
	torrentCmd.Flags().Float64("seed-ratio", 0, "keep seeding after completion until uploaded/size reaches this, e.g. 1.0 (default: stop at completion)")
	torrentCmd.Flags().Duration("seed-time", 0, "keep seeding after completion for this long, e.g. 30m or 2h (default: stop at completion)")
	torrentCmd.Flags().Bool("torbox", false, "download it through TorBox (needs your TorBox API key in Settings)")
	torrentCmd.Flags().Bool("p2p", false, "download it straight from peers, even with \"Use TorBox for new torrents\" on")
	addBatchFlag(torrentCmd)
}
