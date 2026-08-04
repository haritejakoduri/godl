package cmd

import (
	"fmt"
	"net/url"
	"path/filepath"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
	"godl/internal/store"
)

var urlCmd = &cobra.Command{
	Use:   "url <link>",
	Short: "Download a direct HTTP(S) link, resumable and concurrently chunked",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		link := args[0]
		output, _ := cmd.Flags().GetString("output")
		concurrency, _ := cmd.Flags().GetInt("concurrency")

		if output == "" {
			output = filenameFromURL(link)
		}
		abs, err := filepath.Abs(output)
		if err != nil {
			return err
		}
		output = abs

		if err := daemon.EnsureRunning(); err != nil {
			return err
		}
		resp, err := daemon.Call(daemon.Request{
			Cmd:         daemon.CmdAddURL,
			Source:      link,
			Output:      output,
			Concurrency: concurrency,
		})
		if err != nil {
			return err
		}
		if resp.Job.Status == store.StatusFailed {
			return fmt.Errorf("job %s failed immediately: %s", resp.Job.ID, resp.Job.ErrorMsg)
		}
		fmt.Printf("Started job %s -> %s\n", resp.Job.ID, output)
		fmt.Println(`Track it with "godl status" or "godl list".`)
		return nil
	},
}

func filenameFromURL(link string) string {
	if u, err := url.Parse(link); err == nil {
		if base := filepath.Base(u.Path); base != "" && base != "." && base != "/" {
			return base
		}
	}
	return "download"
}

func init() {
	urlCmd.Flags().StringP("output", "o", "", "output file path (default: derived from the URL)")
	urlCmd.Flags().IntP("concurrency", "c", 4, "number of concurrent chunks (ignored if the server can't do ranges)")
}
