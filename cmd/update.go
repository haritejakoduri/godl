package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"godl/internal/ffmpeg"
	"godl/internal/ytdlp"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check for (and install) newer yt-dlp/ffmpeg builds right now",
	Long: `godl normally checks for a newer yt-dlp build at most once a day (and
ffmpeg once a week) automatically, the next time either is needed —
this forces that check immediately instead of waiting. Try this first
if "godl social" starts failing: YouTube and other sites change often
enough that yt-dlp needs to keep up, and a stale bundled copy is the
most common cause.

godl always installs and maintains its own copy of yt-dlp/ffmpeg
(under its data dir), never a system install on PATH, so this always
checks the copy godl itself is actually using.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		printLine := func(msg string) { fmt.Println(" ", msg) }

		fmt.Println("yt-dlp:")
		updated, err := ytdlp.ForceUpdate(ctx, printLine)
		reportUpdateResult(updated, err)

		fmt.Println("ffmpeg:")
		updated, err = ffmpeg.ForceUpdate(ctx, printLine)
		reportUpdateResult(updated, err)

		return nil
	},
}

func reportUpdateResult(updated bool, err error) {
	switch {
	case err != nil:
		fmt.Println("  error:", err)
	case !updated:
		fmt.Println("  already up to date.")
	}
}
