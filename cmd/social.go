package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"godl/internal/daemon"
	"godl/internal/paths"
	"godl/internal/ytdlp"
)

// socialPreset is a named shortcut for a yt-dlp format selector, so
// picking a quality doesn't require knowing yt-dlp's selector syntax.
// Format == "" means "don't pass -f at all" — yt-dlp's own default,
// which is already the best combined stream.
type socialPreset struct {
	Name        string
	Format      string
	Description string
}

var socialPresets = []socialPreset{
	{"best", "", "Best combined quality (yt-dlp's default)"},
	{"1080p", "bv*[height<=1080]+ba/b[height<=1080]", "Cap at 1080p, best audio"},
	{"720p", "bv*[height<=720]+ba/b[height<=720]", "Cap at 720p, best audio"},
	{"480p", "bv*[height<=480]+ba/b[height<=480]", "Cap at 480p, best audio"},
	{"worst", "worst", "Lowest quality (quick preview/test)"},
	{"audio", "bestaudio/best", "Audio only, best available quality"},
}

func lookupSocialPreset(name string) (socialPreset, bool) {
	for _, p := range socialPresets {
		if p.Name == name {
			return p, true
		}
	}
	return socialPreset{}, false
}

// printSocialPresets lists the presets without touching yt-dlp or the
// network — a static, local, always-available complement to
// --list-formats' live per-link probe.
func printSocialPresets() error {
	rows := make([]string, 0, len(socialPresets))
	for _, p := range socialPresets {
		rows = append(rows, fmt.Sprintf("%s\t%s", p.Name, p.Description))
	}
	return printTable("PRESET\tDESCRIPTION", rows)
}

var socialCmd = &cobra.Command{
	Use:   "social <link>",
	Short: "Download from a social/media site via yt-dlp, in the background like other jobs",
	Long: `Download from a social/media site via yt-dlp.

Like url and torrent jobs, this starts in the background and returns
immediately — track it with "godl status" (live progress bar, speed,
ETA) or "godl list". Pass --wait to instead stay attached and stream
yt-dlp's own output live, the way url/torrent don't.

Picking a video/audio resolution — easiest via a named preset with
-p/--preset (see "godl social --list-presets" for the full list):

  godl social <link>                 # best combined quality (default)
  godl social <link> -p 1080p        # cap at 1080p, best audio
  godl social <link> -p 720p         # cap at 720p, best audio
  godl social <link> -p worst        # lowest quality (quick preview/test)
  godl social <link> -p audio        # audio only, best available quality

Or, for full control, pass a yt-dlp format selector directly with
-f/--format instead (not together with -p):

  godl social <link> -f "bv*+ba"                              # best video + best audio, merged (needs ffmpeg — auto-installed)
  godl social <link> -f "bv*[height<=1080]+ba"                # cap at 1080p, best audio
  godl social <link> -f "bv*[height<=720]+ba/b[height<=720]"  # 720p, falling back to a combined stream if separate ones aren't available

Not sure which resolutions/formats exist for a given link? List them
first without downloading anything:

  godl social <link> --list-formats

then pass whichever format code or filter you want via -f.

If yt-dlp isn't found on PATH, godl downloads a standalone copy from its
GitHub releases the first time it's needed (saved under godl's data dir
for reuse) — no separate install step required, though a system install
(pip install -U yt-dlp) is used instead if present. Same goes for
ffmpeg, needed to merge separately-downloaded video+audio streams.`,
	// --list-presets is a static, local list unrelated to any particular
	// link, so (unlike --list-formats, which probes a specific link) it
	// shouldn't require one — a custom Args check relaxes ExactArgs(1)
	// for that one case.
	Args: func(cmd *cobra.Command, args []string) error {
		if listPresets, _ := cmd.Flags().GetBool("list-presets"); listPresets {
			return cobra.MaximumNArgs(1)(cmd, args)
		}
		return cobra.ExactArgs(1)(cmd, args)
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		if listPresets, _ := cmd.Flags().GetBool("list-presets"); listPresets {
			return printSocialPresets()
		}
		link := args[0]

		if listFormats, _ := cmd.Flags().GetBool("list-formats"); listFormats {
			return runListFormats(link)
		}

		output, _ := cmd.Flags().GetString("output")
		format, _ := cmd.Flags().GetString("format")
		preset, _ := cmd.Flags().GetString("preset")
		wait, _ := cmd.Flags().GetBool("wait")

		if preset != "" {
			if format != "" {
				return fmt.Errorf("pass either -p/--preset or -f/--format, not both")
			}
			p, ok := lookupSocialPreset(preset)
			if !ok {
				return fmt.Errorf("unknown preset %q — see \"godl social --list-presets\"", preset)
			}
			format = p.Format
		}
		if output == "" {
			dir, err := paths.DownloadsDir()
			if err != nil {
				return err
			}
			output = dir
		}
		abs, err := resolveOutputPath(output)
		if err != nil {
			return err
		}
		output = abs
		if err := os.MkdirAll(output, 0o755); err != nil {
			return err
		}

		if err := daemon.EnsureRunning(); err != nil {
			return err
		}

		req := daemon.Request{Cmd: daemon.CmdAddSocial, Source: link, Output: output, Format: format}

		if !wait {
			resp, err := daemon.Call(req)
			if err != nil {
				return err
			}
			fmt.Printf("Started job %s -> %s\n", resp.Job.ID, output)
			fmt.Println(`Track it with "godl status" or "godl list".`)
			return nil
		}

		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()

		resp, err := daemon.StreamSocial(ctx, req,
			func(job *daemon.JobView) {
				fmt.Printf("Started job %s -> %s\n", job.ID, output)
			},
			func(line string) {
				fmt.Println(line)
			},
		)
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			fmt.Println("Interrupted; the download continues in the background. Check it with \"godl status\".")
			return nil
		}
		if resp != nil && resp.Job != nil {
			fmt.Printf("Job %s finished.\n", resp.Job.ID)
		}
		return nil
	},
}

// runListFormats prints every format yt-dlp can see for link (id,
// resolution, codec, filesize, ...) without downloading anything or
// creating a daemon job — a quick, synchronous, read-only lookup so the
// user has real format codes/heights to hand -f, rather than guessing.
func runListFormats(link string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ytDlpPath, err := ytdlp.Ensure(ctx, func(msg string) { fmt.Println(msg) })
	if err != nil {
		return err
	}

	c := exec.CommandContext(ctx, ytDlpPath, "-F", link)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func init() {
	socialCmd.Flags().StringP("output", "o", "", "output directory (default: your Downloads folder)")
	socialCmd.Flags().StringP("format", "f", "", `yt-dlp format selector (passed through as -f), e.g. "bv*+ba" or "bv*[height<=1080]+ba" — not together with -p`)
	socialCmd.Flags().StringP("preset", "p", "", "quality preset (see --list-presets); not together with -f")
	socialCmd.Flags().BoolP("wait", "w", false, "stay attached and stream yt-dlp's output live instead of returning immediately")
	socialCmd.Flags().BoolP("list-formats", "F", false, "list available formats/resolutions for <link> and exit, without downloading")
	socialCmd.Flags().Bool("list-presets", false, "list available quality presets and exit")
}
