package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"godl/internal/ffmpeg"
	"godl/internal/selfupdate"
	"godl/internal/ytdlp"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Check for (and install) newer godl/yt-dlp/ffmpeg builds right now",
	Long: `godl normally checks for a newer yt-dlp build at most once a day (and
ffmpeg once a week) automatically, the next time either is needed —
this forces that check immediately instead of waiting. Try this first
if "godl social" starts failing: YouTube and other sites change often
enough that yt-dlp needs to keep up, and a stale bundled copy is the
most common cause.

godl always installs and maintains its own copy of yt-dlp/ffmpeg
(under its data dir), never a system install on PATH, so this always
checks the copy godl itself is actually using.

This also checks for a newer godl release and updates the running
binary in place, on platforms with one published for it (currently
linux/amd64 and darwin/arm64) — everywhere else (Windows, an apt-
installed godl, or another platform entirely) it tells you where to
get the update instead of trying to replace anything itself.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		printLine := func(msg string) { fmt.Println(" ", msg) }

		fmt.Println("godl:")
		result, err := selfupdate.ForceUpdate(ctx, printLine)
		reportSelfUpdateResult(result, err)

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

func reportSelfUpdateResult(result selfupdate.Result, err error) {
	switch {
	case err != nil:
		fmt.Println("  error:", err)
	case result == selfupdate.Updated:
		// selfupdate.ForceUpdate's own progress callback already printed
		// the "updated to X" line.
	case result == selfupdate.AlreadyLatest:
		fmt.Println("  already up to date.")
	case result == selfupdate.ManagedInstall:
		fmt.Println("  installed via apt — run \"sudo apt update && sudo apt upgrade\" instead.")
	default: // Unsupported
		fmt.Printf("  no self-update available for this platform/install — grab the latest from %s\n", releasesURL)
	}
}

const releasesURL = "https://github.com/haritejakoduri/godl/releases/latest"
