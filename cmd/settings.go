package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
	"godl/internal/prefs"
	"godl/internal/store"
)

var settingsCmd = &cobra.Command{
	Use:   "settings",
	Short: "Show and change godl's settings (the same ones as the dashboard's Settings tab)",
	Long: `Show and change godl's settings — the same list the dashboard's
Settings tab ("s" in godl status) edits. Every setting is a default: a
flag on a single command (-o, -c, -p, --seed-ratio, ...) still wins.

  godl settings                               # everything, grouped
  godl settings set download_dir ~/Media
  godl settings set connections 8
  godl settings set social_preset 720p
  godl settings set cookies_from_browser firefox
  godl settings get connections
  godl settings reset connections             # back to the default
  godl settings reset --all`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := appSettings()
		if err != nil {
			return err
		}
		printSettings(s)
		return nil
	},
}

func printSettings(s store.Settings) {
	// Padded by hand rather than with tabwriter, whose alignment would
	// restart at every section header. The value goes last since a
	// folder path can be any length.
	labelW, keyW := 0, 0
	for _, f := range prefs.Fields {
		labelW = max(labelW, len(f.Label))
		keyW = max(keyW, len(f.Key))
	}
	section := ""
	for _, f := range prefs.Fields {
		if f.Section != section {
			if section != "" {
				fmt.Println()
			}
			section = f.Section
			fmt.Println(strings.ToUpper(section))
		}
		value := f.Display(s)
		if f.Overridden() {
			value += " (overridden by " + f.EnvOverride + ")"
		}
		fmt.Printf("  %-*s  %-*s  %s\n", labelW, f.Label, keyW, f.Key, value)
	}
	fmt.Println(`
Change one with "godl settings set <key> <value>", or press "s" in godl status.`)
}

var settingsGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Print one setting's value",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		f, err := lookupSetting(args[0])
		if err != nil {
			return err
		}
		s, err := appSettings()
		if err != nil {
			return err
		}
		fmt.Println(f.Get(s))
		return nil
	},
}

var settingsSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Change a setting (an empty value resets it)",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		f, err := lookupSetting(args[0])
		if err != nil {
			return err
		}
		// Joined so a folder with spaces works unquoted too.
		value := strings.Join(args[1:], " ")
		return changeSettings(func(s *store.Settings) error {
			if err := f.Set(s, value); err != nil {
				return fmt.Errorf("%s: %w", f.Key, err)
			}
			return nil
		}, f)
	},
}

var settingsResetCmd = &cobra.Command{
	Use:   "reset <key> | --all",
	Short: "Put a setting (or all of them) back to its default",
	Args: func(cmd *cobra.Command, args []string) error {
		if all, _ := cmd.Flags().GetBool("all"); all {
			return cobra.NoArgs(cmd, args)
		}
		return cobra.ExactArgs(1)(cmd, args)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if all, _ := cmd.Flags().GetBool("all"); all {
			if err := changeSettings(func(s *store.Settings) error { *s = store.DefaultSettings(); return nil }); err != nil {
				return err
			}
			return nil
		}
		f, err := lookupSetting(args[0])
		if err != nil {
			return err
		}
		return changeSettings(func(s *store.Settings) error { f.Reset(s); return nil }, f)
	},
}

func lookupSetting(key string) (prefs.Field, error) {
	f, ok := prefs.Lookup(key)
	if ok {
		return f, nil
	}
	keys := make([]string, len(prefs.Fields))
	for i, f := range prefs.Fields {
		keys[i] = f.Key
	}
	return prefs.Field{}, fmt.Errorf("unknown setting %q — one of: %s", key, strings.Join(keys, ", "))
}

// changeSettings applies change to the saved settings and saves them
// through the daemon, which validates and applies them immediately.
// shown are the fields to report afterwards (all of them when none).
func changeSettings(change func(*store.Settings) error, shown ...prefs.Field) error {
	s, err := appSettings()
	if err != nil {
		return err
	}
	if err := change(&s); err != nil {
		return err
	}
	resp, err := daemon.Call(daemon.Request{Cmd: daemon.CmdSetSettings, Settings: &s})
	if err != nil {
		return err
	}
	saved := *resp.Settings
	cachedSettings = &saved
	if len(shown) == 0 {
		printSettings(saved)
		return nil
	}
	for _, f := range shown {
		fmt.Printf("%s = %s\n", f.Key, f.Display(saved))
		if f.Overridden() {
			fmt.Printf("note: %s is set, so it still wins over this setting\n", f.EnvOverride)
		}
	}
	return nil
}

func init() {
	settingsResetCmd.Flags().Bool("all", false, "reset every setting")
	settingsCmd.AddCommand(settingsGetCmd, settingsSetCmd, settingsResetCmd)
}
