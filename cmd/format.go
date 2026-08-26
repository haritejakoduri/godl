package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"godl/internal/ratelimit"
	"godl/internal/store"
)

func humanBytes(n int64) string {
	if n < 0 {
		return "?"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func humanSpeed(bps float64) string {
	if bps <= 0 {
		return "-"
	}
	return humanBytes(int64(bps)) + "/s"
}

func humanETA(seconds int64, status store.JobStatus) string {
	if status != store.StatusActive || seconds < 0 {
		return "-"
	}
	d := time.Duration(seconds) * time.Second
	if d > 99*time.Hour {
		return ">99h"
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

func percent(done, total int64) float64 {
	if total <= 0 {
		return 0
	}
	p := float64(done) / float64(total)
	if p > 1 {
		p = 1
	}
	if p < 0 {
		p = 0
	}
	return p
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

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

// resolveOutputPath resolves output to an absolute path. An empty
// output is passed through unchanged so callers can distinguish "user
// didn't set -o" from "user set -o to something" after the call.
func resolveOutputPath(output string) (string, error) {
	if output == "" {
		return "", nil
	}
	abs, err := filepath.Abs(output)
	if err != nil {
		return "", err
	}
	return abs, nil
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
