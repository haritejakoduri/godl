package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
	"godl/internal/paths"
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

// outputPath resolves the shared -o/--output flag to an absolute path.
// An unset flag defaults to the user's Downloads folder; name, when
// given, is the filename to place inside it (commands that download a
// single file pass one, commands that download into a directory don't).
func outputPath(flag, name string) (string, error) {
	if flag == "" {
		dir, err := paths.DownloadsDir()
		if err != nil {
			return "", err
		}
		flag = filepath.Join(dir, name)
	}
	return paths.ResolveOutput(flag)
}

// startJob hands req to the daemon, starting it first if it isn't
// already running, and announces the job it created.
func startJob(req daemon.Request) error {
	if err := daemon.EnsureRunning(); err != nil {
		return err
	}
	resp, err := daemon.Call(req)
	if err != nil {
		return err
	}
	if resp.Job.Status == store.StatusFailed {
		return fmt.Errorf("job %s failed immediately: %s", resp.Job.ID, resp.Job.ErrorMsg)
	}
	announceJob(resp.Job.ID, req.Output)
	return nil
}

func announceJob(id, output string) {
	fmt.Printf("Started job %s -> %s\n", id, output)
	fmt.Println(`Track it with "godl status" or "godl list".`)
}
