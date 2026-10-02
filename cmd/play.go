package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"godl/internal/mpv"
)

var playCmd = &cobra.Command{
	Use:   "play <link>",
	Short: "Stream a social/media link in mpv/VLC without downloading it",
	Long: `Stream a yt-dlp-supported link (YouTube and the like) straight in
mpv, or VLC if mpv isn't installed — no job, nothing saved to disk.
One of the two players has to be installed already; godl supplies its
own yt-dlp to resolve the link.

Quality works like "godl social": a preset with -p/--preset (see
"godl social --list-presets"), or a yt-dlp format selector with
-f/--format.

  godl play <link>              # best quality
  godl play <link> -p 720p      # cap at 720p
  godl play <link> -p audio     # audio only

To keep the file as well, use "godl social" — pressing "o" on the job
in "godl status" plays it while it downloads.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		format, err := formatFlags(cmd)
		if err != nil {
			return err
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		if err := mpv.PlayLink(ctx, args[0], format, func(msg string) { fmt.Println(msg) }); err != nil {
			return err
		}
		player, _ := mpv.Name()
		fmt.Printf("Playing %s in %s\n", args[0], player)
		return nil
	},
}

func init() {
	playCmd.Flags().StringP("preset", "p", "", `quality preset (see "godl social --list-presets"); not together with -f`)
	playCmd.Flags().StringP("format", "f", "", "yt-dlp format selector, as in \"godl social -f\"; not together with -p")
}
