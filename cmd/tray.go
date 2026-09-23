package cmd

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"godl/internal/autostart"
	"godl/internal/daemon"
	"godl/tray"
)

var trayCmd = &cobra.Command{
	Use:   daemon.TrayCommand,
	Short: "Show godl in the system tray, with a menu to stop the daemon",
	Long: `Show a system tray icon for godl's background daemon.

The daemon already does this for itself: it puts an icon up when it
starts, so you normally never need this command. Turn that off in the
Settings tab of "godl status" if you would rather it didn't.

Run by hand, this shows an icon that outlives the daemon, offering to
start it again — useful if you turned the automatic one off, or want an
icon up before any download exists.

The menu reports what the daemon is doing and can open the dashboard,
pause or resume every job, and stop the daemon.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		install, _ := cmd.Flags().GetBool("install-autostart")
		uninstall, _ := cmd.Flags().GetBool("uninstall-autostart")
		attached, _ := cmd.Flags().GetBool(daemon.TrayAttachedFlag)
		switch {
		case install && uninstall:
			return errors.New("pass either --install-autostart or --uninstall-autostart, not both")
		case install:
			return setAutostart(true)
		case uninstall:
			return setAutostart(false)
		}

		if attached {
			// Spawned by the daemon rather than asked for, so it stays
			// quiet about every reason an icon might not appear.
			return tray.Attach()
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
	trayCmd.Flags().Bool(daemon.TrayAttachedFlag, false, "internal: tie this tray's lifetime to the daemon that spawned it")
	trayCmd.Flags().MarkHidden(daemon.TrayAttachedFlag)
}
