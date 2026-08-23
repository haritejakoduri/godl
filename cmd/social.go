package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
	"godl/internal/ytdlp"
)

var socialCmd = &cobra.Command{
	Use:   "social <link>",
	Short: "Download from a social/media site via yt-dlp, in the background like other jobs",
	Long: `Download from a social/media site via yt-dlp.

Like url and torrent jobs, this starts in the background and returns
immediately — track it with "godl status" (live progress bar, speed,
ETA) or "godl list". Pass --wait to instead stay attached and stream
yt-dlp's own output live, the way url/torrent don't.

Picking a video/audio resolution — the -f/--format flag is passed
straight through to yt-dlp's own format selector, so anything yt-dlp
accepts there works here:

  godl social <link>                                     # best combined quality (default)
  godl social <link> -f worst                            # lowest quality (quick preview/test)
  godl social <link> -f "bv*+ba"                          # best video + best audio, merged (needs ffmpeg — auto-installed)
  godl social <link> -f "bv*[height<=1080]+ba"            # cap at 1080p, best audio
  godl social <link> -f "bv*[height<=720]+ba/b[height<=720]"  # 720p, falling back to a combined stream if separate ones aren't available

Not sure which resolutions/formats exist for a given link? List them
first without downloading anything:

  godl social <link> --list-formats

then pass whichever format code or filter you want via -f.

If yt-dlp isn't found on PATH, godl downloads a standalone copy from its
GitHub releases the first time it's needed (saved under godl's data dir
for reuse) — no separate install step required, though a system install
(pip install -U yt-dlp) is used instead if present. Same goes for
ffmpeg, needed to merge separately-downloaded video+audio streams.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		link := args[0]

		if listFormats, _ := cmd.Flags().GetBool("list-formats"); listFormats {
			return runListFormats(link)
		}

		output, _ := cmd.Flags().GetString("output")
		format, _ := cmd.Flags().GetString("format")
		wait, _ := cmd.Flags().GetBool("wait")
		if output == "" {
			output = "."
		}
		abs, err := resolveOutputPath(output)
		if err != nil {
			return err
		}
		output = abs
		if err := os.MkdirAll(output, 0o755); err != nil {
			return err
		}

		if err := daemon.EnsureRunning(); err != nil {
			return err
		}

		req := daemon.Request{Cmd: daemon.CmdAddSocial, Source: link, Output: output, Format: format}

		if !wait {
			resp, err := daemon.Call(req)
			if err != nil {
				return err
			}
			fmt.Printf("Started job %s -> %s\n", resp.Job.ID, output)
			fmt.Println(`Track it with "godl status" or "godl list".`)
			return nil
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		resp, err := daemon.StreamSocial(ctx, req,
			func(job *daemon.JobView) {
				fmt.Printf("Started job %s -> %s\n", job.ID, output)
			},
			func(line string) {
				fmt.Println(line)
			},
		)
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			fmt.Println("Interrupted; the download continues in the background. Check it with \"godl status\".")
			return nil
		}
		if resp != nil && resp.Job != nil {
			fmt.Printf("Job %s finished.\n", resp.Job.ID)
		}
		return nil
	},
}

// runListFormats prints every format yt-dlp can see for link (id,
// resolution, codec, filesize, ...) without downloading anything or
// creating a daemon job — a quick, synchronous, read-only lookup so the
// user has real format codes/heights to hand -f, rather than guessing.
func runListFormats(link string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ytDlpPath, err := ytdlp.Ensure(ctx, func(msg string) { fmt.Println(msg) })
	if err != nil {
		return err
	}

	c := exec.CommandContext(ctx, ytDlpPath, "-F", link)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func init() {
	socialCmd.Flags().StringP("output", "o", ".", "output directory")
	socialCmd.Flags().StringP("format", "f", "", `yt-dlp format selector (passed through as -f), e.g. "bv*+ba" or "bv*[height<=1080]+ba"`)
	socialCmd.Flags().BoolP("wait", "w", false, "stay attached and stream yt-dlp's output live instead of returning immediately")
	socialCmd.Flags().BoolP("list-formats", "F", false, "list available formats/resolutions for <link> and exit, without downloading")
}
