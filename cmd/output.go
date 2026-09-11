package cmd

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"godl/internal/ratelimit"
	"godl/internal/store"
)

// limitRateFlag reads the shared --limit-rate string flag (registered
// by url/social/torrent/webdav) and parses it into bytes/second, with
// "flag not set" (empty string, the default) meaning unlimited rather
// than an error.
func limitRateFlag(cmd *cobra.Command) (int64, error) {
	s, _ := cmd.Flags().GetString("limit-rate")
	if s == "" {
		return 0, nil
	}
	return ratelimit.ParseRate(s)
}

// statusCell renders a job's status for a plain-text table cell,
// appending the failure reason when there is one — a bare "failed"
// with no explanation is what made LocalSend (and any other job type)
// failures look like silent, unexplained nothing.
func statusCell(status store.JobStatus, errMsg string) string {
	if status == store.StatusFailed && errMsg != "" {
		return fmt.Sprintf("failed: %s", errMsg)
	}
	return string(status)
}

// printTable writes a tab-separated table to stdout: header is the
// column header line (tab-separated, no trailing newline), rows are
// each row's already tab-separated body line (no trailing newline).
func printTable(header string, rows []string) error {
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, header)
	for _, r := range rows {
		fmt.Fprintln(w, r)
	}
	return w.Flush()
}
