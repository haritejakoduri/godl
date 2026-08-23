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

Only applies to godl's own auto-installed copies, not a system install
already on PATH — updating that is your OS package manager's job.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		printLine := func(msg string) { fmt.Println(" ", msg) }

		fmt.Println("yt-dlp:")
		managed, updated, err := ytdlp.ForceUpdate(ctx, printLine)
		reportUpdateResult(managed, updated, err)

		fmt.Println("ffmpeg:")
		managed, updated, err = ffmpeg.ForceUpdate(ctx, printLine)
		reportUpdateResult(managed, updated, err)

		return nil
	},
}

func reportUpdateResult(managed, updated bool, err error) {
	switch {
	case err != nil:
		fmt.Println("  error:", err)
	case !managed:
		fmt.Println("  using a system install on PATH; not managed by godl.")
	case !updated:
		fmt.Println("  already up to date.")
	}
}
