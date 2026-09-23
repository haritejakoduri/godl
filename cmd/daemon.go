package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
	"godl/internal/paths"
	"godl/internal/store"
)

// daemonCmd groups the commands for managing the background daemon
// directly. Nothing here is needed in normal use — every other command
// starts the daemon on demand — but a long-running background process
// the user can't see or stop is its own problem, which is what "godl
// tray" solves on the desktop and these solve everywhere else.
var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Manage the background daemon (status, start, stop)",
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report whether the daemon is running",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		sock, err := paths.SocketPath()
		if err != nil {
			return err
		}
		if !daemon.Running() {
			fmt.Println("godl daemon: not running")
			return nil
		}
		resp, err := daemon.Call(daemon.Request{Cmd: daemon.CmdList})
		if err != nil {
			return err
		}
		var active, queued int
		for _, j := range resp.Jobs {
			switch j.Status {
			case store.StatusActive:
				active++
			case store.StatusQueued:
				queued++
			}
		}
		fmt.Printf("godl daemon: running (%s)\n", sock)
		fmt.Printf("  %d job(s): %d active, %d queued\n", len(resp.Jobs), active, queued)
		return nil
	},
}

var daemonStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the daemon if it isn't already running",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if daemon.Running() {
			fmt.Println("godl daemon: already running")
			return nil
		}
		if err := daemon.EnsureRunning(); err != nil {
			return err
		}
		fmt.Println("godl daemon: started")
		return nil
	},
}

var daemonStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the daemon, leaving unfinished jobs to resume next time",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		stopped, err := daemon.Stop()
		if err != nil {
			return err
		}
		if !stopped {
			fmt.Println("godl daemon: not running")
			return nil
		}
		fmt.Println("godl daemon: stopped")
		return nil
	},
}

func init() {
	daemonCmd.AddCommand(daemonStatusCmd, daemonStartCmd, daemonStopCmd)
}
