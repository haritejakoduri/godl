package cmd

import (
	"github.com/spf13/cobra"

	"godl/tui"
)

// statusCmd is the cobra wrapper around the dashboard. The TUI itself
// lives in package tui — this file exists only to attach it to the
// command tree, the same way a GUI front end would get its own thin
// entry point without either knowing about the other.
var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Full-screen dashboard of all jobs, with live progress, speed, and ETA",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return tui.Run()
	},
}
