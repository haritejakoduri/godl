package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"godl/internal/appwindow"
	"godl/internal/daemon"
	"godl/internal/fileserver"
	"godl/internal/guide"
	"godl/internal/paths"
	"godl/internal/store"
	"godl/internal/webui"
)

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "Open godl's web interface in a window of its own (and turn it on if it's off)",
	Long: `godl's web interface does everything the terminal dashboard does:
start and manage downloads, pick a torrent's files, browse WebDAV
servers, share a folder, change settings, and watch videos in the page
with a choice of audio language.

It opens in a window of its own — no tabs, no address bar, its own icon
in the dock or taskbar. That window comes from a Chromium-family
browser (Chrome, Edge, Chromium, Brave, Vivaldi) running in app mode,
which is why it may not be the browser you have set as default. Where
there is no such browser — a Mac with only Safari, say — it opens as an
ordinary tab instead, and --browser asks for that on purpose.

It is off until you turn it on, and is served by godl's background
daemon, so it stays available after this command returns.

  godl web                  # turn it on if needed, and open its window
  godl web --browser        # open it as a browser tab instead
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
		asTab, _ := cmd.Flags().GetBool("browser")
		https, _ := cmd.Flags().GetBool("https")
		noHTTPS, _ := cmd.Flags().GetBool("no-https")
		if network && local {
			return fmt.Errorf("pass either --network or --local, not both")
		}
		if https && noHTTPS {
			return fmt.Errorf("pass either --https or --no-https, not both")
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
			if https {
				s.WebUIHTTPS = true
			}
			if noHTTPS {
				s.WebUIHTTPS = false
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
			if s.WebUIHTTPS && !s.WebUINetwork {
				return fmt.Errorf("--https applies to --network only: on this machine the address is 127.0.0.1, which browsers already treat as secure, and a self-signed certificate there would only produce a warning")
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
		// Loopback is always plain http, even with https on: see
		// store.Settings.WebUIHTTPS.
		open := fmt.Sprintf("http://127.0.0.1:%d/", port)
		if s.WebUINetwork {
			scheme := "http"
			if s.WebUIHTTPS {
				scheme = "https"
				open = fmt.Sprintf("https://127.0.0.1:%d/", port)
			}
			fmt.Printf("godl's web interface is on, for this machine and your network (sign in as %q):\n", s.WebUIUsername)
			fmt.Printf("  %s\n", open)
			for _, ip := range fileserver.ReachableIPs() {
				fmt.Printf("  %s://%s:%d/\n", scheme, ip, port)
			}
			if s.WebUIHTTPS {
				fmt.Println()
				fmt.Println("It is served with a certificate godl made itself, so each device will")
				fmt.Println("refuse the page until you tell it to trust that certificate once:")
				for _, ip := range fileserver.ReachableIPs() {
					fmt.Printf("  https://%s:%d%s\n", ip, port, webui.CertPath)
					break
				}
				fmt.Println("Open that on the device, then install it (on Android: Settings, then")
				fmt.Println("search for \"CA certificate\"). Until then the page will not load there.")
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
		if asTab {
			if err := guide.OpenInBrowser(open); err != nil {
				fmt.Println("Couldn't open a browser by itself — open that address in one.")
			}
			return nil
		}
		// Only the fallback is worth a word: a window that appeared
		// needs no announcement, but a tab where one was expected
		// does, or it looks like the feature is broken.
		if _, ok := appwindow.Browser(); !ok {
			fmt.Println("Opening it as a browser tab — install Chrome, Edge or Chromium for a window of its own.")
		}
		if err := appwindow.Open(open); err != nil {
			fmt.Println("Couldn't open it by itself — open that address in a browser.")
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
	webCmd.Flags().Bool("no-open", false, "print the address without opening anything")
	webCmd.Flags().Bool("browser", false, "open it as a tab in your default browser, not in a window of its own")
	webCmd.Flags().Bool("https", false, "with --network, serve over TLS so passwords aren't sent in the clear (needs trusting godl's certificate on each device)")
	webCmd.Flags().Bool("no-https", false, "with --network, go back to plain http")
}
