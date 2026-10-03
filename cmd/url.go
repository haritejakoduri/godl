package cmd

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
	"godl/internal/jobreq"
	"godl/internal/urlname"
)

// sha256Pattern matches a bare hex sha256 digest. Checked up front so a
// mistyped --sha256 value fails immediately instead of after a full
// download that was always going to be rejected.
var sha256Pattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

var urlCmd = &cobra.Command{
	Use:   "url <link> | -i <links-file>",
	Short: "Download a direct HTTP(S) link, resumable and concurrently chunked",
	Args:  batchArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		all, err := links(cmd, args)
		if err != nil {
			return err
		}
		output, _ := cmd.Flags().GetString("output")
		concurrency, _ := cmd.Flags().GetInt("concurrency")
		sha256Sum, _ := cmd.Flags().GetString("sha256")
		if sha256Sum != "" && !sha256Pattern.MatchString(sha256Sum) {
			return fmt.Errorf("--sha256 must be a 64-character hex digest, got %q", sha256Sum)
		}
		if sha256Sum != "" && len(all) > 1 {
			return fmt.Errorf("--sha256 checks a single file; it can't be used with several links")
		}
		limitRate, err := limitRateFlag(cmd)
		if err != nil {
			return err
		}
		opts, err := requestOptions(cmd)
		if err != nil {
			return err
		}
		hdr, err := requestHeaders(opts)
		if err != nil {
			return err
		}

		outputs, err := urlOutputs(all, output, func(link string) string { return urlname.FromURL(link, hdr) })
		if err != nil {
			return err
		}
		reqs := make([]daemon.Request, len(all))
		for i, link := range all {
			reqs[i] = daemon.Request{
				Cmd:         daemon.CmdAddURL,
				Source:      link,
				Output:      outputs[i],
				Concurrency: concurrency,
				LimitRate:   limitRate,
				Sha256:      strings.ToLower(sha256Sum),
				Options:     opts,
			}
		}
		return startJobs(reqs)
	},
}

// urlOutputs is jobreq.URLOutputs: one link keeps -o as a file path,
// several treat it as the directory they all land in.
func urlOutputs(all []string, output string, nameOf func(string) string) ([]string, error) {
	return jobreq.URLOutputs(all, output, nameOf)
}

func init() {
	urlCmd.Flags().StringP("output", "o", "", "output file path (default: your Downloads folder, with a name derived from the URL or the server's Content-Disposition/Content-Type); with several links, the directory they're saved into")
	urlCmd.Flags().IntP("concurrency", "c", 4, "number of concurrent chunks (ignored if the server can't do ranges)")
	urlCmd.Flags().StringP("limit-rate", "R", "", "cap this download's speed, e.g. 500K or 2M (default: unlimited)")
	urlCmd.Flags().String("sha256", "", "expected sha256 digest of the completed file; on mismatch the file is deleted and the job fails (default: no verification)")
	addRequestFlags(urlCmd)
	addBatchFlag(urlCmd)
}
