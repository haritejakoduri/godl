package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
	"godl/internal/fileserver"
	"godl/internal/guide"
	"godl/internal/paths"
	"godl/internal/store"
	"godl/internal/webui"
)

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "Open godl's web interface in your browser (and turn it on if it's off)",
	Long: `godl's web interface does everything the terminal dashboard does, in a
browser: start and manage downloads, pick a torrent's files, browse
WebDAV servers, share a folder, change settings, and watch videos in
the page with a choice of audio language.

It is off until you turn it on, and is served by godl's background
daemon, so it stays available after this command returns.

  godl web                  # turn it on if needed, and open it
  godl web --off            # turn it off

By default only this machine can reach it. To use it from a phone or
another computer on your network, give it a username and password:

  godl web --network --username alice    # asks for a password
  godl web --local                       # back to this machine only

Over the network it is plain http, like "godl serve": fine on your own
home network, not for the open internet.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		off, _ := cmd.Flags().GetBool("off")
		network, _ := cmd.Flags().GetBool("network")
		local, _ := cmd.Flags().GetBool("local")
		noOpen, _ := cmd.Flags().GetBool("no-open")
		if network && local {
			return fmt.Errorf("pass either --network or --local, not both")
		}

		if err := daemon.EnsureRunning(); err != nil {
			return err
		}
		resp, err := daemon.Call(daemon.Request{Cmd: daemon.CmdGetSettings})
		if err != nil {
			return err
		}
		s := *resp.Settings
		before := s

		if off {
			s.WebUI = false
		} else {
			s.WebUI = true
			if cmd.Flags().Changed("port") {
				s.WebUIPort, _ = cmd.Flags().GetInt("port")
			}
			if local {
				s.WebUINetwork = false
			}
			if network {
				s.WebUINetwork = true
			}
			if u, _ := cmd.Flags().GetString("username"); u != "" {
				s.WebUIUsername = u
			}
			password, _ := cmd.Flags().GetString("password")
			if password == "" {
				password = os.Getenv("GODL_WEB_PASSWORD")
			}
			if password != "" {
				s.WebUIPassword = password
			}
			if s.WebUINetwork && s.WebUIUsername == "" {
				return fmt.Errorf("--network needs --username (and a password): anyone who could reach the page could otherwise start and delete downloads")
			}
			if s.WebUINetwork && s.WebUIPassword == "" {
				p, err := promptPassword("Password for the web interface: ")
				if err != nil {
					return fmt.Errorf("reading password: %w (pass --password or set GODL_WEB_PASSWORD for non-interactive use)", err)
				}
				s.WebUIPassword = p
			}
		}

		if s != before {
			if _, err := daemon.Call(daemon.Request{Cmd: daemon.CmdSetSettings, Settings: &s}); err != nil {
				return err
			}
		}
		if off {
			fmt.Println("godl's web interface is off.")
			return nil
		}

		port := s.WebUIPort
		if port == 0 {
			port = store.DefaultWebUIPort
		}
		open := fmt.Sprintf("http://127.0.0.1:%d/", port)
		if s.WebUINetwork {
			fmt.Printf("godl's web interface is on, for this machine and your network (sign in as %q):\n", s.WebUIUsername)
			fmt.Printf("  %s\n", open)
			for _, ip := range fileserver.ReachableIPs() {
				fmt.Printf("  http://%s:%d/\n", ip, port)
			}
		} else {
			dataDir, err := paths.DataDir()
			if err != nil {
				return err
			}
			tok, err := webui.Token(dataDir)
			if err != nil {
				return err
			}
			// The token is what lets this browser in; see webui.Auth.
			open += "?token=" + tok
			fmt.Printf("godl's web interface is on, for this machine only:\n  %s\n", open)
		}
		if noOpen {
			return nil
		}
		if err := guide.OpenInBrowser(open); err != nil {
			fmt.Println("Couldn't open a browser by itself — open that address in one.")
		}
		return nil
	},
}

func init() {
	webCmd.Flags().Bool("off", false, "turn the web interface off")
	webCmd.Flags().Bool("network", false, "let other devices on your network use it (needs --username and a password)")
	webCmd.Flags().Bool("local", false, "only this machine may use it (the default)")
	webCmd.Flags().String("username", "", "username for --network")
	webCmd.Flags().String("password", "", "password for --network (prompted if omitted; or set GODL_WEB_PASSWORD)")
	webCmd.Flags().Int("port", store.DefaultWebUIPort, "port to serve it on")
	webCmd.Flags().Bool("no-open", false, "print the address without opening a browser")
}
