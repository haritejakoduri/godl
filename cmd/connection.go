package cmd

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"godl/internal/connections"
)

var connectionCmd = &cobra.Command{
	Use:     "connection",
	Aliases: []string{"connections", "conn"},
	Short:   "Manage saved connections to remote storage (WebDAV today; more providers planned)",
	Args:    cobra.NoArgs,
}

var connectionNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

var connectionAddCmd = &cobra.Command{
	Use:   "add <name>",
	Short: "Save a WebDAV connection's credentials for later use",
	Long: `Saves a named WebDAV connection so "godl webdav <name> <remote-path>"
can download from it without re-entering credentials every time.

Credentials are stored under godl's data directory (~/.local/share/godl
by default), in a file only your user account can read. Running this
again with an existing name overwrites that connection's settings.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if !connectionNameRe.MatchString(name) {
			return fmt.Errorf("connection name must contain only letters, digits, - and _")
		}
		url, _ := cmd.Flags().GetString("url")
		username, _ := cmd.Flags().GetString("username")
		password, _ := cmd.Flags().GetString("password")
		insecure, _ := cmd.Flags().GetBool("insecure")

		if url == "" {
			return fmt.Errorf(`--url is required, e.g. --url https://dav.example.com/remote.php/dav/files/alice/`)
		}
		if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
			return fmt.Errorf("--url must start with http:// or https://")
		}
		if password == "" {
			password = os.Getenv("GODL_WEBDAV_PASSWORD")
		}
		if password == "" {
			p, err := promptPassword("WebDAV password: ")
			if err != nil {
				return fmt.Errorf("reading password: %w (pass --password or set GODL_WEBDAV_PASSWORD for non-interactive use)", err)
			}
			password = p
		}

		c := connections.Connection{
			Name:     name,
			Type:     connections.TypeWebDAV,
			URL:      url,
			Username: username,
			Password: password,
			Insecure: insecure,
		}
		if err := connections.Add(c); err != nil {
			return err
		}
		fmt.Printf("Saved connection %q (webdav, %s)\n", name, url)
		return nil
	},
}

var connectionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List saved connections",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		list, err := connections.List()
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println(`No connections yet. Try "godl connection add <name> --url ... --username ..."`)
			return nil
		}
		rows := make([]string, 0, len(list))
		for _, c := range list {
			user := c.Username
			if user == "" {
				user = "-"
			}
			rows = append(rows, fmt.Sprintf("%s\t%s\t%s\t%s", c.Name, c.Type, user, c.URL))
		}
		return printTable("NAME\tTYPE\tUSERNAME\tURL", rows)
	},
}

var connectionRemoveCmd = &cobra.Command{
	Use:     "remove <name>",
	Aliases: []string{"rm"},
	Short:   "Remove a saved connection",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := connections.Remove(args[0]); err != nil {
			return err
		}
		fmt.Printf("Removed connection %q\n", args[0])
		return nil
	},
}

// promptPassword reads a password from the terminal without echoing it.
func promptPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func init() {
	connectionAddCmd.Flags().String("url", "", "WebDAV base URL, must start with http:// or https://")
	connectionAddCmd.Flags().String("username", "", "WebDAV username")
	connectionAddCmd.Flags().String("password", "", "WebDAV password (omit to be prompted, or set GODL_WEBDAV_PASSWORD)")
	connectionAddCmd.Flags().Bool("insecure", false, "skip TLS certificate verification (self-signed https servers)")
	connectionCmd.AddCommand(connectionAddCmd, connectionListCmd, connectionRemoveCmd)
}
