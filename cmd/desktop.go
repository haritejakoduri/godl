package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"godl/internal/desktop"
)

var desktopCmd = &cobra.Command{
	Use:   "desktop",
	Short: "Add godl to your desktop's application menu (Linux)",
	Long: `Add godl to your desktop's application menu, with its icon.

Installing the .deb or .rpm does this for you. Use this when godl came
from the raw binary or install.sh, or to point an existing entry at a
binary you have since moved.

  godl desktop install
  godl desktop remove
  godl desktop status

Everything is written under ~/.local/share, so no root is needed and it
affects only your own account.`,
}

var desktopInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Add the application menu entry and icons",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := desktop.Install(); err != nil {
			return err
		}
		_, where, err := desktop.Status()
		if err != nil {
			fmt.Println("Added godl to your application menu.")
			return nil
		}
		fmt.Printf("Added godl to your application menu: %s\n", where)
		fmt.Println(`Search for "godl" in your launcher. It opens the web interface.`)
		return nil
	},
}

var desktopRemoveCmd = &cobra.Command{
	Use:   "remove",
	Short: "Remove the application menu entry and icons",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := desktop.Remove(); err != nil {
			return err
		}
		fmt.Println("Removed godl from your application menu.")
		return nil
	},
}

var desktopStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report whether the menu entry is installed",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		installed, where, err := desktop.Status()
		if err != nil {
			return err
		}
		if installed {
			fmt.Printf("Installed: %s\n", where)
			return nil
		}
		fmt.Printf("Not installed (would be %s)\nRun \"godl desktop install\" to add it.\n", where)
		return nil
	},
}

func init() {
	desktopCmd.AddCommand(desktopInstallCmd, desktopRemoveCmd, desktopStatusCmd)
}
