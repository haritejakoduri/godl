// Package cmd wires up godl's cobra subcommands.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"godl/internal/version"
)

var rootCmd = &cobra.Command{
	Use:   "godl",
	Short: "A terminal download manager for direct links, torrents, and social/media sites",
	Long: `godl downloads direct HTTP(S) links, torrents, and yt-dlp-supported
social/media links through a background daemon, so jobs keep running
after you close the terminal.

Run "godl" with no arguments for a live TUI dashboard (start new
downloads, track progress), or use the pause/resume/retry/cancel/
remove/list subcommands to script it.`,
	Version:       version.Version,
	SilenceUsage:  true,
	SilenceErrors: true,
	Args:          cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runStatusTUI()
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "godl:", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(
		urlCmd,
		socialCmd,
		torrentCmd,
		statusCmd,
		pauseCmd,
		resumeCmd,
		retryCmd,
		cancelCmd,
		removeCmd,
		listCmd,
		updateCmd,
		internalDaemonCmd,
	)
}
