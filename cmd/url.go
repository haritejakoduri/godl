package cmd

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
	"godl/internal/paths"
	"godl/internal/store"
	"godl/internal/urlname"
)

// sha256Pattern matches a bare hex sha256 digest. Checked up front so a
// mistyped --sha256 value fails immediately instead of after a full
// download that was always going to be rejected.
var sha256Pattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

var urlCmd = &cobra.Command{
	Use:   "url <link>",
	Short: "Download a direct HTTP(S) link, resumable and concurrently chunked",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		link := args[0]
		output, _ := cmd.Flags().GetString("output")
		concurrency, _ := cmd.Flags().GetInt("concurrency")
		sha256Sum, _ := cmd.Flags().GetString("sha256")
		if sha256Sum != "" && !sha256Pattern.MatchString(sha256Sum) {
			return fmt.Errorf("--sha256 must be a 64-character hex digest, got %q", sha256Sum)
		}
		limitRate, err := limitRateFlag(cmd)
		if err != nil {
			return err
		}

		if output == "" {
			dir, err := paths.DownloadsDir()
			if err != nil {
				return err
			}
			output = filepath.Join(dir, urlname.FromURL(link))
		}
		abs, err := paths.ResolveOutput(output)
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
			LimitRate:   limitRate,
			Sha256:      strings.ToLower(sha256Sum),
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

func init() {
	urlCmd.Flags().StringP("output", "o", "", "output file path (default: your Downloads folder, with a name derived from the URL or the server's Content-Disposition/Content-Type)")
	urlCmd.Flags().IntP("concurrency", "c", 4, "number of concurrent chunks (ignored if the server can't do ranges)")
	urlCmd.Flags().StringP("limit-rate", "R", "", "cap this download's speed, e.g. 500K or 2M (default: unlimited)")
	urlCmd.Flags().String("sha256", "", "expected sha256 digest of the completed file; on mismatch the file is deleted and the job fails (default: no verification)")
}
