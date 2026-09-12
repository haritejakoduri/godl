package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"godl/internal/autostart"
	"godl/tray"
)

var trayCmd = &cobra.Command{
	Use:   "tray",
	Short: "Show godl in the system tray, with a menu to stop the daemon",
	Long: `Show a system tray icon for godl's background daemon.

The icon reports what the daemon is doing and its menu can open the
dashboard, pause or resume every job, and stop the daemon — which is
otherwise invisible once started.

Runs in the foreground until the icon is dismissed. Use
--install-autostart to have it start at login instead.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		install, _ := cmd.Flags().GetBool("install-autostart")
		uninstall, _ := cmd.Flags().GetBool("uninstall-autostart")
		switch {
		case install && uninstall:
			return errors.New("pass either --install-autostart or --uninstall-autostart, not both")
		case install:
			return setAutostart(true)
		case uninstall:
			return setAutostart(false)
		}

		if err := tray.Run(); err != nil {
			// Both of these mean "there is no tray here", which is a
			// situation with a perfectly good answer rather than a
			// dead end — so say what it is.
			if errors.Is(err, tray.ErrUnsupported) || errors.Is(err, tray.ErrNoSession) {
				return fmt.Errorf("%w\n\nUse \"godl daemon status\" and \"godl daemon stop\" instead", err)
			}
			return err
		}
		return nil
	},
}

// setAutostart registers or removes the login entry and says where it
// went, since that location is the thing a user would otherwise have to
// go looking for to undo this by hand.
func setAutostart(on bool) error {
	action, verb := autostart.Uninstall, "removed"
	if on {
		action, verb = autostart.Install, "installed"
	}
	if err := action(); err != nil {
		return err
	}
	_, location, err := autostart.Status()
	if err != nil {
		fmt.Printf("Autostart entry %s.\n", verb)
		return nil
	}
	fmt.Printf("Autostart entry %s: %s\n", verb, location)
	return nil
}

func init() {
	trayCmd.Flags().Bool("install-autostart", false, "register the tray to start at login, then exit")
	trayCmd.Flags().Bool("uninstall-autostart", false, "remove the login entry, then exit")
}
