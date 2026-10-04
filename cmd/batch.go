package cmd

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
	"godl/internal/jobreq"
	"godl/internal/store"
)

// addBatchFlag registers -i/--input-file on a command that normally
// takes one link.
func addBatchFlag(cmd *cobra.Command) {
	cmd.Flags().StringP("input-file", "i", "", `read links from this file, one per line ("-" for stdin); blank lines and # comments are skipped`)
}

// batchArgs is the Args check for commands with addBatchFlag: exactly
// one link normally, or any number (including none) alongside -i.
func batchArgs(cmd *cobra.Command, args []string) error {
	if f, _ := cmd.Flags().GetString("input-file"); f != "" {
		return nil
	}
	return cobra.ExactArgs(1)(cmd, args)
}

// links returns every link to download: the positional args followed by
// whatever -i supplied.
func links(cmd *cobra.Command, args []string) ([]string, error) {
	out := append([]string(nil), args...)
	f, _ := cmd.Flags().GetString("input-file")
	if f == "" {
		return out, nil
	}
	var r io.Reader = cmd.InOrStdin()
	if f != "-" {
		file, err := os.Open(f)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		r = file
	}
	fromFile, err := readLinks(r)
	if err != nil {
		return nil, err
	}
	out = append(out, fromFile...)
	if len(out) == 0 {
		return nil, fmt.Errorf("no links found in %s", f)
	}
	return out, nil
}

// readLinks parses a links list: one per line, trimmed, with blank lines
// and lines starting with # ignored.
func readLinks(r io.Reader) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, sc.Err()
}

// startJobs starts every request, carrying on past individual failures
// so one bad link doesn't stop the rest of a batch, and reports how it
// went. A single request behaves exactly like startJob.
func startJobs(reqs []daemon.Request) error {
	if len(reqs) == 1 {
		return startJob(reqs[0])
	}
	if err := daemon.EnsureRunning(); err != nil {
		return err
	}
	started, failed := 0, 0
	for _, req := range reqs {
		resp, err := daemon.Call(req)
		switch {
		case err != nil:
			failed++
			fmt.Fprintf(os.Stderr, "failed: %s: %v\n", req.Source, err)
		case resp.Job.Status == store.StatusFailed:
			failed++
			fmt.Fprintf(os.Stderr, "failed: %s: %s\n", req.Source, resp.Job.ErrorMsg)
		default:
			started++
			fmt.Printf("Started job %s -> %s\n", resp.Job.ID, req.Output)
		}
	}
	fmt.Printf("%d job(s) started", started)
	if failed > 0 {
		fmt.Printf(", %d failed", failed)
	}
	fmt.Println(`. Track them with "godl status" or "godl list".`)
	if failed > 0 {
		return fmt.Errorf("%d of %d link(s) could not be started", failed, len(reqs))
	}
	return nil
}

// uniqueName is jobreq.UniqueName.
func uniqueName(name string, taken map[string]bool) string {
	return jobreq.UniqueName(name, taken)
}
