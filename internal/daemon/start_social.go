package daemon

import (
	"bytes"
	"context"
	"os/exec"
	"strconv"
	"strings"

	"godl/internal/ffmpeg"
	"godl/internal/store"
	"godl/internal/ytdlp"
)

// ytdlpArgs builds the yt-dlp command line for j, minus the source URL:
// progress reporting, format selection, rate limiting and ffmpeg
// discovery. Progress and resolved-path lines come back over stdout for
// lineWriter to parse — see godlProgressPrefix.
func (d *Daemon) ytdlpArgs(ctx context.Context, j *store.Job) []string {
	args := []string{
		"--newline", "-P", j.Output,
		// Machine-readable progress on its own line, so we can feed
		// it into the same byte-count/speed tracking url and
		// torrent jobs use instead of scraping the human-readable
		// "[download] 54.1% of 180MiB at 12MiB/s" text. Filtered
		// out of the human-visible log by godlProgressPrefix below.
		"--progress-template", "download:" + godlProgressPrefix + "%(progress.status)s|%(progress.downloaded_bytes)s|%(progress.total_bytes)s|%(progress.total_bytes_estimate)s",
		// Fires during/after postprocessing (merging separate
		// video+audio, embedding thumbnails, renaming, ...) with the
		// true final path — unlike the progress hook above, which
		// for a merged download reports the intermediate video/
		// audio files that get deleted right after merging. Lets
		// "godl remove --purge" know exactly what to delete later.
		// Also filtered out of the human-visible log.
		//
		// This used to be `--print "after_move:..."`, which is the
		// more semantically-precise hook for "give me the final
		// path once" — but combining --print with --progress-template
		// silently kills the download-progress hook entirely (empirically
		// confirmed against yt-dlp 2026.07.04: 0 progress lines with
		// --print present vs. the expected 11 without it, regardless
		// of flag order). A second --progress-template of type
		// postprocess sidesteps the conflict by staying within the
		// one feature, and empirically never reports the
		// soon-to-be-deleted intermediate files either — it can fire
		// more than once for the same final path (once per
		// postprocessor stage), which AppendResolvedPath already
		// no-ops on if the path repeats.
		"--progress-template", "postprocess:" + godlFilePrefix + "%(info.filepath)s",
	}
	if j.Format != "" {
		args = append(args, "-f", j.Format)
	}
	// yt-dlp has its own native rate limiter — no need to
	// reimplement one for a subprocess we don't read the bytes of
	// ourselves. Same clamp-not-share treatment as torrent's global
	// cap: whichever of this job's own rate and the global cap is
	// more restrictive is what yt-dlp actually gets told.
	if effective := minPositiveRate(j.LimitRate, d.cachedGlobalRateLimitBps()); effective > 0 {
		args = append(args, "--limit-rate", strconv.FormatInt(effective, 10))
	}
	// ffmpeg is needed to merge separately-downloaded video+audio
	// streams (common with -f "bv*+ba" style selectors). Its
	// absence isn't fatal to the job — yt-dlp just leaves the
	// streams unmerged and warns — so a failure here is logged, not
	// treated as a job error.
	if ffmpegDir, err := ffmpeg.Ensure(ctx, func(msg string) { d.publishLog(j.ID, msg, false) }); err == nil {
		args = append(args, "--ffmpeg-location", ffmpegDir)
	} else {
		d.publishLog(j.ID, "warning: "+err.Error()+" — separately downloaded video/audio streams won't be merged", false)
	}
	return args
}

func (d *Daemon) startSocial(j *store.Job) {
	d.launch(j, func(ctx context.Context, rt *runtime) {
		ytDlpPath, err := ytdlp.Ensure(ctx, func(msg string) { d.publishLog(j.ID, msg, false) })
		if err != nil {
			d.publishLog(j.ID, "error: "+err.Error(), true)
			d.finishJob(j.ID, 0, false, err)
			return
		}

		args := append(d.ytdlpArgs(ctx, j), j.Source)

		cmd := exec.CommandContext(ctx, ytDlpPath, args...)
		lw := &lineWriter{onLine: func(line string) {
			if done, total, ok := parseGodlProgress(line); ok {
				d.reportProgress(j.ID, done, total, nil)
				return
			}
			if path, ok := parseGodlFile(line); ok {
				d.st.AppendResolvedPath(context.Background(), j.ID, path)
				return
			}
			d.publishLog(j.ID, line, false)
		}}
		cmd.Stdout = lw
		cmd.Stderr = lw

		runErr := cmd.Run()
		lw.flush()

		rt.mu.Lock()
		finalBytes := rt.bytesDone
		rt.mu.Unlock()

		if runErr != nil && ctx.Err() != nil {
			d.publishLog(j.ID, "", true)
			d.finishJob(j.ID, finalBytes, false, context.Canceled)
			return
		}
		if runErr != nil {
			d.publishLog(j.ID, "error: "+runErr.Error(), true)
			d.finishJob(j.ID, finalBytes, false, runErr)
			return
		}
		d.publishLog(j.ID, "", true)
		d.finishJob(j.ID, finalBytes, true, nil)
	})
}

// godlProgressPrefix tags the machine-readable progress lines produced
// by yt-dlp's --progress-template so they can be told apart from its
// normal human-readable output.
const godlProgressPrefix = "GODLPROGRESS "

// parseGodlProgress parses a
// "GODLPROGRESS <status>|<downloaded>|<total>|<total_estimate>" line
// (fields are "NA" when yt-dlp doesn't know them) into byte counts.
// total falls back to the estimate when the exact size isn't known.
//
// When yt-dlp finds the destination file already fully present (e.g.
// re-running on something already downloaded), it fires this hook
// exactly once with status=finished but downloaded_bytes=NA — it
// didn't transfer anything, so it has no byte count for it, even
// though total_bytes is usually still known. Without special-casing
// that, this job's progress would never be recorded at all and
// list/status would show "-" forever despite completing successfully.
// Treated as fully done (using total for both) instead.
//
// Multi-stream downloads (e.g. -f "bv*+ba") report progress per
// stream, not as one smooth job-wide percentage — a video stream
// finishing and an audio stream starting will show as a reset to a
// lower percentage, which is an accurate reflection of what's actually
// happening rather than a bug.
func parseGodlProgress(line string) (done, total int64, ok bool) {
	if !strings.HasPrefix(line, godlProgressPrefix) {
		return 0, 0, false
	}
	fields := strings.Split(strings.TrimPrefix(line, godlProgressPrefix), "|")
	if len(fields) != 4 {
		return 0, 0, false
	}
	status, doneField, totalField, estimateField := fields[0], fields[1], fields[2], fields[3]

	total, err := strconv.ParseInt(totalField, 10, 64)
	if err != nil {
		total, err = strconv.ParseInt(estimateField, 10, 64)
		if err != nil {
			total = 0
		}
	}

	done, err = strconv.ParseInt(doneField, 10, 64)
	if err != nil {
		if status == "finished" && total > 0 {
			return total, total, true
		}
		return 0, 0, false
	}
	return done, total, true
}

// godlFilePrefix tags the after_move print-hook lines from yt-dlp
// carrying a final resolved output file path (see startSocial).
const godlFilePrefix = "GODLFILE "

func parseGodlFile(line string) (path string, ok bool) {
	if !strings.HasPrefix(line, godlFilePrefix) {
		return "", false
	}
	return strings.TrimPrefix(line, godlFilePrefix), true
}

// lineWriter buffers partial writes and invokes onLine for each complete
// line, so a subprocess's raw byte stream can be forwarded line-by-line.
type lineWriter struct {
	buf    []byte
	onLine func(string)
}

func (lw *lineWriter) Write(p []byte) (int, error) {
	lw.buf = append(lw.buf, p...)
	for {
		idx := bytes.IndexByte(lw.buf, '\n')
		if idx < 0 {
			break
		}
		line := string(bytes.TrimRight(lw.buf[:idx], "\r"))
		lw.buf = lw.buf[idx+1:]
		if lw.onLine != nil && line != "" {
			lw.onLine(line)
		}
	}
	return len(p), nil
}

func (lw *lineWriter) flush() {
	if len(lw.buf) > 0 && lw.onLine != nil {
		lw.onLine(string(lw.buf))
		lw.buf = nil
	}
}
