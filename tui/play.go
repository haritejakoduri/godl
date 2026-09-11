package tui

import (
	"fmt"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"

	"godl/internal/connections"
	"godl/internal/daemon"
	"godl/internal/mpv"
	"godl/internal/store"
	"godl/internal/webdav"
)

// localPlaybackTarget returns the local file already on disk for j, if
// it's done downloading — preferred over streaming from source
// whenever it's available. The source (a direct HTTP link, a WebDAV
// file behind a saved connection) can need auth or a time-limited
// token that's gone stale by the time playback is actually requested
// — e.g. a signed CDN mirror link that only stays valid for a short
// window after the page that generated it was loaded — while the file
// already on disk needs nothing further from the network and is
// guaranteed to exist. ok is false when the job isn't done yet, or
// (torrent) nothing was ever resolved — callers fall back to streaming
// in that case, which for torrent jobs is unconditionally rejected
// instead (no piece-sequencing here — see doPlay).
func localPlaybackTarget(j *store.Job) (target string, ok bool) {
	if j.Status != store.StatusCompleted {
		return "", false
	}
	switch j.Type {
	case store.JobURL:
		if j.Output == "" {
			return "", false
		}
		return j.Output, true
	case store.JobTorrent:
		if len(j.ResolvedPaths) == 0 {
			return "", false
		}
		return filepath.Join(j.Output, j.ResolvedPaths[0]), true
	case store.JobSocial, store.JobWebDAV:
		if len(j.ResolvedPaths) == 0 {
			return "", false
		}
		return j.ResolvedPaths[0], true
	default:
		return "", false
	}
}

// doPlay streams or plays j with mpv (or VLC, if mpv isn't installed).
// A completed job always plays its local file (see
// localPlaybackTarget) rather than re-streaming — url/social jobs
// otherwise hand mpv the original source URL directly (true streaming,
// no local download needed, since mpv's own network stack — plus its
// bundled yt-dlp hook for social links — handles it), and webdav jobs
// resolve the saved connection and stream the remote file the same
// way, authenticated via an HTTP header rather than a URL-embedded
// password. Both are genuinely useful for a job that's still actively
// downloading — a preview while it finishes — which is the only case
// they're still reachable for now. torrent jobs only ever support the
// local file, and only once the job has completed — true
// streaming-while-downloading needs piece-sequencing anacrolix/torrent
// doesn't do here.
func doPlay(j *daemon.JobView) tea.Cmd {
	return func() tea.Msg {
		if target, ok := localPlaybackTarget(j.Job); ok {
			return playedMsg{target: target, err: mpv.Play(target, nil)}
		}
		switch j.Type {
		case store.JobURL, store.JobSocial:
			return playedMsg{target: j.Source, err: mpv.Play(j.Source, nil)}

		case store.JobWebDAV:
			connName, remotePath, ok := daemon.SplitWebDAVSource(j.Source)
			if !ok {
				return playedMsg{err: fmt.Errorf("invalid webdav job source %q", j.Source)}
			}
			conn, err := connections.Get(connName)
			if err != nil {
				return playedMsg{err: err}
			}
			client, err := webdav.New(conn.URL, conn.Username, conn.Password, conn.Insecure)
			if err != nil {
				return playedMsg{err: err}
			}
			target := client.URLFor(remotePath).String()
			var headers map[string]string
			if auth := client.AuthHeader(); auth != "" {
				headers = map[string]string{"Authorization": auth}
			}
			return playedMsg{target: target, err: mpv.Play(target, headers)}

		case store.JobTorrent:
			return playedMsg{err: fmt.Errorf("streaming isn't available for torrents until the job completes (playback order isn't sequential mid-download)")}

		default:
			return playedMsg{err: fmt.Errorf("streaming isn't supported for %s jobs", j.Type)}
		}
	}
}
