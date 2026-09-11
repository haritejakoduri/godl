package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"godl/internal/daemon"
	"godl/internal/paths"
	"godl/internal/urlname"
)

// doBulkJobAction fires apiCmd (pause/resume/cancel/retry) against
// every job in jobIDs, sequentially — these are lightweight
// control-plane RPCs (not data transfer), so there's no throughput
// reason to parallelize, and sequential keeps the daemon's per-job
// state transitions easy to reason about under a bulk request. See
// bulkActionDoneMsg for how partial failure is reported.
func doBulkJobAction(apiCmd string, jobIDs []string) tea.Cmd {
	return func() tea.Msg {
		var ok, failed int
		var firstErr error
		for _, id := range jobIDs {
			if _, err := daemon.Call(daemon.Request{Cmd: apiCmd, JobID: id}); err != nil {
				failed++
				if firstErr == nil {
					firstErr = err
				}
			} else {
				ok++
			}
		}
		return bulkActionDoneMsg{n: len(jobIDs), ok: ok, failed: failed, err: firstErr}
	}
}

func doBulkRemove(jobIDs []string, purge bool) tea.Cmd {
	return func() tea.Msg {
		var ok, failed int
		var firstErr error
		for _, id := range jobIDs {
			if _, err := daemon.Call(daemon.Request{Cmd: daemon.CmdRemove, JobID: id, Purge: purge}); err != nil {
				failed++
				if firstErr == nil {
					firstErr = err
				}
			} else {
				ok++
			}
		}
		return bulkActionDoneMsg{n: len(jobIDs), ok: ok, failed: failed, err: firstErr}
	}
}

// buildAddRequest fills in a daemon.Request for apiCmd (CmdAddURL/
// CmdAddSocial/CmdAddTorrent) from source, applying the same output
// defaults as the corresponding CLI command (see url.go/social.go/
// torrent.go's RunE) — so a download started from the TUI behaves
// exactly like "godl url"/"godl social"/"godl torrent".
func buildAddRequest(apiCmd, source string) (daemon.Request, error) {
	switch apiCmd {
	case daemon.CmdAddURL:
		dir, err := paths.DownloadsDir()
		if err != nil {
			return daemon.Request{}, err
		}
		output, err := paths.ResolveOutput(filepath.Join(dir, urlname.FromURL(source)))
		if err != nil {
			return daemon.Request{}, err
		}
		return daemon.Request{Cmd: apiCmd, Source: source, Output: output, Concurrency: 4}, nil

	case daemon.CmdAddSocial:
		dir, err := paths.DownloadsDir()
		if err != nil {
			return daemon.Request{}, err
		}
		output, err := paths.ResolveOutput(dir)
		if err != nil {
			return daemon.Request{}, err
		}
		return daemon.Request{Cmd: apiCmd, Source: source, Output: output}, nil

	case daemon.CmdAddTorrent:
		def, err := paths.DownloadsDir()
		if err != nil {
			return daemon.Request{}, err
		}
		output, err := paths.ResolveOutput(def)
		if err != nil {
			return daemon.Request{}, err
		}
		if !strings.HasPrefix(source, "magnet:") {
			abs, err := paths.ResolveOutput(source)
			if err != nil {
				return daemon.Request{}, err
			}
			source = abs
		}
		return daemon.Request{Cmd: apiCmd, Source: source, Output: output}, nil

	default:
		return daemon.Request{}, fmt.Errorf("unknown job type %q", apiCmd)
	}
}

// startNewJob starts apiCmd's job for source. format is only meaningful
// for CmdAddSocial (the chosen preset's yt-dlp format selector, or ""
// for the default); buildAddRequest never sets Format for url/torrent,
// and the daemon ignores Format for those job types, so passing it
// through unconditionally is harmless for them.
func startNewJob(apiCmd, source, format string) tea.Cmd {
	return func() tea.Msg {
		if err := daemon.EnsureRunning(); err != nil {
			return actionDoneMsg{err}
		}
		req, err := buildAddRequest(apiCmd, source)
		if err != nil {
			return actionDoneMsg{err}
		}
		req.Format = format
		_, err = daemon.Call(req)
		return actionDoneMsg{err}
	}
}
