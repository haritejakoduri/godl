package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
	"godl/internal/mpv"
	"godl/internal/store"
)

var streamCmd = &cobra.Command{
	Use:   "stream <job-id>",
	Short: "Play a torrent job in mpv/VLC while it's still downloading",
	Long: `Play a torrent job in mpv (or VLC) while it's still downloading.

godl prioritizes the pieces the player is about to read, including
after a seek, so playback starts within seconds instead of waiting for
the whole file. With several files in the torrent it picks the largest
video/audio file; choose another with --file (numbered as in
"godl torrent --list-files"). A finished job plays from disk.

  godl stream 1a2b3c4d
  godl stream 1a2b3c4d --file 3
  godl stream 1a2b3c4d --url     # just print the URL, for another player`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fileNum, _ := cmd.Flags().GetInt("file")
		printOnly, _ := cmd.Flags().GetBool("url")
		if err := daemon.EnsureRunning(); err != nil {
			return err
		}
		job, err := findJob(args[0])
		if err != nil {
			return err
		}
		if job.Type != store.JobTorrent {
			return fmt.Errorf("job %s is a %s job; godl stream is for torrents (use \"o\" in godl status for the others)", job.ID, job.Type)
		}

		target := ""
		if job.Status == store.StatusCompleted && len(job.ResolvedPaths) > 0 {
			target = filepath.Join(job.Output, job.ResolvedPaths[0])
		} else {
			resp, err := daemon.Call(daemon.Request{Cmd: daemon.CmdStreamTorrent, JobID: job.ID, FileIndex: fileNum})
			if err != nil {
				return err
			}
			target = resp.StreamURL
		}
		if printOnly {
			fmt.Println(target)
			return nil
		}
		if err := mpv.Play(target, nil); err != nil {
			return err
		}
		fmt.Printf("Playing %s\n", target)
		return nil
	},
}

// findJob looks a job up by ID in the daemon's list.
func findJob(id string) (*daemon.JobView, error) {
	resp, err := daemon.Call(daemon.Request{Cmd: daemon.CmdList})
	if err != nil {
		return nil, err
	}
	for _, j := range resp.Jobs {
		if j.ID == id {
			return j, nil
		}
	}
	return nil, fmt.Errorf("job %s not found", id)
}

func init() {
	streamCmd.Flags().Int("file", 0, "which file to play, numbered as in \"godl torrent --list-files\" (default: the largest video/audio file)")
	streamCmd.Flags().Bool("url", false, "print the stream URL instead of launching a player")
}
