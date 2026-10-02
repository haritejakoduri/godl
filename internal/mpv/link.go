package mpv

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"godl/internal/ytdlp"
)

// resolveTimeout bounds the yt-dlp call VLC playback needs. It only
// fetches the page's stream metadata, not media, so a slow answer means
// the site is unhappy rather than that the work is large.
const resolveTimeout = 30 * time.Second

// ensureYtdlp is ytdlp.Ensure, swappable so tests never download it.
var ensureYtdlp = ytdlp.Ensure

// PlayLink plays a yt-dlp-supported page link (a YouTube watch page,
// say) straight from the site, with nothing written to disk. format is
// a yt-dlp format selector as in "godl social -f"; empty means the
// player's best.
//
// The two players get there differently. mpv resolves such a link
// itself through its ytdl hook, so it's handed the link plus godl's own
// managed yt-dlp to run — the hook otherwise needs one on PATH, which
// a machine that only has godl's copy doesn't have. VLC has no
// equivalent, and handed the page URL plays nothing, so godl resolves
// the direct stream URLs with yt-dlp first.
//
// progress reports a first-use yt-dlp download; it may be nil.
func PlayLink(ctx context.Context, link, format string, progress func(string)) error {
	// Before yt-dlp: with no player there's nothing to fetch it for.
	playerPath, kind, err := findPlayer()
	if err != nil {
		return err
	}
	ytDlpPath, err := ensureYtdlp(ctx, progress)
	if err != nil {
		return fmt.Errorf("getting yt-dlp to resolve the link: %w", err)
	}

	if kind == playerMPV {
		return launch(playerPath, append(mpvLinkArgs(ytDlpPath, format), link))
	}
	urls, err := resolveStream(ctx, ytDlpPath, link, format)
	if err != nil {
		return fmt.Errorf("resolving a stream URL for VLC: %w", err)
	}
	return launch(playerPath, vlcLinkArgs(urls))
}

// mpvLinkArgs are mpv's flags for playing a page link. The window is
// forced open at once: resolving takes a few seconds in which mpv
// otherwise shows nothing, and an audio-only format would never open
// one at all — leaving a detached player with no way to stop it.
func mpvLinkArgs(ytDlpPath, format string) []string {
	args := []string{
		// -append, not --script-opts: that one splits its value on
		// commas, which a path may contain.
		"--script-opts-append=ytdl_hook-ytdl_path=" + ytDlpPath,
		"--force-window=immediate",
	}
	if format != "" {
		args = append(args, "--ytdl-format="+format)
	}
	return args
}

// vlcLinkArgs turns the stream URLs yt-dlp resolved into VLC's
// arguments. Most sites answer with one URL. One that serves video and
// audio separately (YouTube, at every resolution) answers with two,
// video first, and VLC plays the pair when the audio rides along as an
// input slave.
func vlcLinkArgs(urls []string) []string {
	if len(urls) < 2 {
		return urls
	}
	return []string{urls[0], "--input-slave=" + urls[1]}
}

// resolveStream asks yt-dlp for link's direct stream URL(s) in format,
// one per line of its -g output.
func resolveStream(ctx context.Context, ytDlpPath, link, format string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	args := []string{"-g"}
	if format != "" {
		args = append(args, "-f", format)
	}
	out, err := exec.CommandContext(ctx, ytDlpPath, append(args, link)...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if msg := lastLine(string(exitErr.Stderr)); msg != "" {
				return nil, errors.New(msg)
			}
		}
		return nil, err
	}
	urls := strings.Fields(string(out))
	if len(urls) == 0 {
		return nil, fmt.Errorf("yt-dlp returned no URL")
	}
	return urls, nil
}

// lastLine is yt-dlp's actual complaint: its stderr leads with
// warnings and ends with the ERROR line.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
