package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"godl/internal/desktop"
)

var desktopCmd = &cobra.Command{
	Use:   "desktop",
	Short: "Add godl to your system's application menu",
	Long: `Add godl to your system's application menu, with its icon.

  godl desktop install
  godl desktop remove
  godl desktop status

What that means depends on the system:

  Linux     a .desktop entry and hicolor icons under ~/.local/share
  Windows   a Start Menu shortcut under your own profile
  macOS     a godl.app bundle in ~/Applications

All of them are per-user, so no administrator rights are needed and
nothing outside your own account is touched. Opening the entry starts
godl's web interface.

The .deb and .rpm already do the Linux half at install time, and the
Windows installer does the Start Menu shortcut. Use this when godl came
from a raw binary, or to point an existing entry at a binary you have
since moved.`,
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
