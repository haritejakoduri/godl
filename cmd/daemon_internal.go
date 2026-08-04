package cmd

import (
	"github.com/spf13/cobra"

	"godl/internal/daemon"
)

// internalDaemonCmd is how godl re-execs itself to run the background
// daemon (see daemon.EnsureRunning). It's not meant to be run by hand.
var internalDaemonCmd = &cobra.Command{
	Use:    daemon.InternalDaemonArg,
	Hidden: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return daemon.RunForeground()
	},
}
