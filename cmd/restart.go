package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
	"godl/internal/version"
)

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart godl's background daemon",
	Long: `Restart godl's background daemon.

Downloads that were running carry on from where they were once it's
back. godl does this by itself after an update, the next time you run
any command; this is for doing it by hand.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := daemon.Restart(); err != nil {
			return err
		}
		fmt.Printf("godl's background daemon restarted (version %s).\n", version.Version)
		return nil
	},
}
