package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"path"
	"sort"
	"strconv"
	"strings"
)

// A Source is something the page's player can be pointed at: a file on
// disk, or an HTTP address ffmpeg can read (a WebDAV file, a torrent
// still downloading through the daemon's loopback stream server, or
// the streams yt-dlp resolved for a web link).
type Source struct {
	Name string

	// Exactly one of Path and URL is set.
	Path string
	URL  string
	// Header is sent with every request for URL (a WebDAV connection's
	// credentials). Never shown to the browser.
	Header http.Header
	// UserAgent, when set, is what the site handed the URLs out to.
	UserAgent string

	// Link sources only. A site that serves picture and sound
	// separately gives one audio input per language; URL is then the
	// picture alone. Empty means URL carries its own audio.
	Audio []AudioInput
	Subs  []SubInput
	// Remote is true for a web link: there's no file to hand out, so
	// "open in another app" means a remux rather than the original.
	Remote bool
	// ChunkSize > 0 means the site slows down long requests, so URL and
	// the audio URLs are read through the relay in pieces of this size
	// (see relay.go).
	ChunkSize int64
}

type AudioInput struct {
	URL      string
	Language string
	Title    string
	Codec    string
}

type SubInput struct {
	URL      string
	Language string
	Title    string
}

// Info is what the page needs to know before it can play a Source.
type Info struct {
	Name     string   `json:"name"`
	Duration float64  `json:"duration"` // seconds, 0 if unknown
	Video    *Video   `json:"video"`    // nil for audio-only
	Audio    []Track  `json:"audio"`
	Subs     []Track  `json:"subs"`
	Direct   bool     `json:"direct"` // the file can be handed to the browser as it is
	Notes    []string `json:"notes,omitempty"`
}

type Video struct {
	Codec  string `json:"codec"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	// MIME is what the page asks the browser about ("can you decode
	// this?") before trying to play — godl never re-encodes picture,
	// so a no here means the fallback links, not a slower stream.
	MIME string `json:"mime"`
}

type Track struct {
	Index    int    `json:"index"` // among tracks of its own kind
	Language string `json:"language"`
	Title    string `json:"title"`
	Codec    string `json:"codec"`
	Channels int    `json:"channels,omitempty"`
	// Text is false for picture-based subtitles (Blu-ray PGS, DVD),
	// which can't become WebVTT.
	Text bool `json:"text,omitempty"`
}

// ffprobeOutput is the slice of "ffprobe -show_format -show_streams"
// this package reads.
type ffprobeOutput struct {
	Format struct {
		Duration   string `json:"duration"`
		FormatName string `json:"format_name"`
	} `json:"format"`
	Streams []struct {
		CodecType   string            `json:"codec_type"`
		CodecName   string            `json:"codec_name"`
		Profile     string            `json:"profile"`
		Level       int               `json:"level"`
		Width       int               `json:"width"`
		Height      int               `json:"height"`
		PixFmt      string            `json:"pix_fmt"`
		Channels    int               `json:"channels"`
		Tags        map[string]string `json:"tags"`
		Disposition map[string]int    `json:"disposition"`
	} `json:"streams"`
}

// inputArgs are the ffmpeg/ffprobe flags that open src's main input,
// ending with "-i <input>". seek > 0 starts there (before -i, so it's a
// fast keyframe seek rather than a decode-and-discard).
func inputArgs(src Source, input string, seek float64) []string {
	var args []string
	if src.Path == "" {
		if src.UserAgent != "" {
			args = append(args, "-user_agent", src.UserAgent)
		}
		if h := headerLines(src.Header); h != "" {
			args = append(args, "-headers", h)
		}
		// A dropped connection mid-film shouldn't end the stream.
		args = append(args, "-reconnect", "1", "-reconnect_streamed", "1", "-reconnect_delay_max", "5")
	}
	if seek > 0 {
		args = append(args, "-ss", strconv.FormatFloat(seek, 'f', 3, 64))
	}
	return append(args, "-i", input)
}

func (s Source) input() string {
	if s.Path != "" {
		return s.Path
	}
	return s.URL
}

func headerLines(h http.Header) string {
	if len(h) == 0 {
		return ""
	}
	names := make([]string, 0, len(h))
	for name := range h {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		for _, v := range h[name] {
			b.WriteString(name + ": " + v + "\r\n")
		}
	}
	return b.String()
}

// probe asks ffprobe what's inside src.
func probe(ctx context.Context, ffprobePath string, src Source) (Info, error) {
	args := []string{"-v", "error", "-print_format", "json", "-show_format", "-show_streams"}
	in := inputArgs(src, src.input(), 0)
	// ffprobe takes the input as a bare argument, not after -i.
	args = append(args, in[:len(in)-2]...)
	args = append(args, src.input())
	out, err := exec.CommandContext(ctx, ffprobePath, args...).Output()
	if err != nil {
		return Info{}, fmt.Errorf("reading %s: %w", src.Name, cmdError(err))
	}
	var p ffprobeOutput
	if err := json.Unmarshal(out, &p); err != nil {
		return Info{}, fmt.Errorf("reading %s: %w", src.Name, err)
	}
	return infoFromProbe(src.Name, p), nil
}

func infoFromProbe(name string, p ffprobeOutput) Info {
	info := Info{Name: name}
	info.Duration, _ = strconv.ParseFloat(p.Format.Duration, 64)
	for _, s := range p.Streams {
		switch s.CodecType {
		case "video":
			// Cover art in an audio file is a "video" stream too.
			if s.Disposition["attached_pic"] == 1 || info.Video != nil {
				continue
			}
			info.Video = &Video{
				Codec: s.CodecName, Width: s.Width, Height: s.Height,
				MIME: videoMIME(s.CodecName, s.Profile, s.Level, s.PixFmt),
			}
		case "audio":
			info.Audio = append(info.Audio, Track{
				Index: len(info.Audio), Language: s.Tags["language"], Title: s.Tags["title"],
				Codec: s.CodecName, Channels: s.Channels,
			})
		case "subtitle":
			info.Subs = append(info.Subs, Track{
				Index: len(info.Subs), Language: s.Tags["language"], Title: s.Tags["title"],
				Codec: s.CodecName, Text: textSubCodecs[s.CodecName],
			})
		}
	}
	info.Direct = directPlayable(name, info)
	return info.withEmptyLists()
}

// withEmptyLists makes "no tracks" an empty list rather than null, so
// the page can always iterate what it's given.
func (i Info) withEmptyLists() Info {
	if i.Audio == nil {
		i.Audio = []Track{}
	}
	if i.Subs == nil {
		i.Subs = []Track{}
	}
	return i
}

var textSubCodecs = map[string]bool{
	"subrip": true, "srt": true, "ass": true, "ssa": true, "webvtt": true, "mov_text": true, "text": true,
}

// directExts are containers a browser reads by itself, with the audio
// codecs it can decode inside them.
var directExts = map[string]bool{
	".mp4": true, ".m4v": true, ".webm": true,
	".mp3": true, ".m4a": true, ".ogg": true, ".opus": true, ".flac": true, ".wav": true,
}

var directAudioCodecs = map[string]bool{
	"aac": true, "mp3": true, "opus": true, "vorbis": true, "flac": true,
	"pcm_s16le": true, "pcm_s24le": true,
}

// directPlayable reports whether the file can go to the browser
// untouched: a container it reads, and no choice of audio to make (a
// browser plays the first track and offers no way to pick another).
// Whether it can decode the picture is the page's call, via Video.MIME.
func directPlayable(name string, info Info) bool {
	if !directExts[strings.ToLower(path.Ext(name))] || len(info.Audio) > 1 {
		return false
	}
	return len(info.Audio) == 0 || directAudioCodecs[info.Audio[0].Codec]
}

// videoMIME builds the "video/mp4; codecs=..." string for a codec
// ffmpeg can copy into MP4 and a browser might decode, or "" for one
// no browser plays (MPEG-4 ASP, MPEG-2, VC-1, ...). The codec strings
// only need to be right enough for a browser's can-you-play answer.
func videoMIME(codec, profile string, level int, pixFmt string) string {
	tenBit := strings.Contains(pixFmt, "10")
	var c string
	switch codec {
	case "h264":
		idc, ok := h264Profiles[profile]
		if !ok {
			idc = 0x64
		}
		if level <= 0 {
			level = 40
		}
		c = fmt.Sprintf("avc1.%02x00%02x", idc, level)
	case "hevc":
		if level <= 0 {
			level = 120
		}
		if tenBit || strings.Contains(profile, "10") {
			c = fmt.Sprintf("hvc1.2.4.L%d.B0", level)
		} else {
			c = fmt.Sprintf("hvc1.1.6.L%d.B0", level)
		}
	case "vp9":
		c = "vp09.00.10.08"
		if tenBit {
			c = "vp09.02.10.10"
		}
	case "av1":
		c = "av01.0.08M.08"
		if tenBit {
			c = "av01.0.08M.10"
		}
	default:
		return ""
	}
	return `video/mp4; codecs="` + c + `"`
}

var h264Profiles = map[string]int{
	"Constrained Baseline": 0x42, "Baseline": 0x42, "Main": 0x4d, "Extended": 0x58,
	"High": 0x64, "High 10": 0x6e, "High 4:2:2": 0x7a, "High 4:4:4 Predictive": 0xf4,
}

// streamArgs builds the ffmpeg command line that repackages src for
// the page's player: fragmented MP4 on stdout, starting at seek, with
// audio track number audio.
//
// The picture is always copied, never re-encoded — that's the whole
// cost model of the player (a remux is a few percent of one core; an
// encode is all of several). Sound is copied when it's already AAC and
// otherwise converted to stereo AAC, which is cheap, and necessary:
// the AC-3 and DTS tracks films ship with play in no browser.
func streamArgs(src Source, info Info, audio int, seek float64) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	args = append(args, inputArgs(src, src.input(), seek)...)

	audioMap, audioCodec := "", ""
	switch {
	case len(src.Audio) > 0:
		if audio < 0 || audio >= len(src.Audio) {
			audio = 0
		}
		args = append(args, inputArgs(src, src.Audio[audio].URL, seek)...)
		audioMap, audioCodec = "1:a:0", src.Audio[audio].Codec
	case len(info.Audio) > 0:
		if audio < 0 || audio >= len(info.Audio) {
			audio = 0
		}
		audioMap, audioCodec = "0:a:"+strconv.Itoa(audio), info.Audio[audio].Codec
	}

	if info.Video != nil {
		args = append(args, "-map", "0:v:0", "-c:v", "copy")
		if info.Video.Codec == "hevc" {
			// Browsers that play HEVC want the hvc1 sample entry; the
			// hev1 one ffmpeg writes by default plays nowhere.
			args = append(args, "-tag:v", "hvc1")
		}
	}
	if audioMap != "" {
		args = append(args, "-map", audioMap)
		if isAAC(audioCodec) {
			args = append(args, "-c:a", "copy")
		} else {
			args = append(args, "-c:a", "aac", "-b:a", "160k", "-ac", "2")
		}
	}
	return append(args,
		"-sn", "-dn", "-map_metadata", "-1", "-map_chapters", "-1",
		// Fragmented, with the header up front: playable from a pipe,
		// no seeking back to finish the file.
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-f", "mp4", "pipe:1")
}

func isAAC(codec string) bool {
	return codec == "aac" || strings.HasPrefix(codec, "mp4a")
}

// remuxAllArgs builds the stream another app (VLC and the like) gets
// for a web link: Matroska carrying the picture and every audio
// language, all copied, so the choice of language moves to that app.
func remuxAllArgs(src Source) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	args = append(args, inputArgs(src, src.input(), 0)...)
	for _, a := range src.Audio {
		args = append(args, inputArgs(src, a.URL, 0)...)
	}
	args = append(args, "-map", "0:v:0?")
	if len(src.Audio) == 0 {
		args = append(args, "-map", "0:a?")
	}
	for i, a := range src.Audio {
		args = append(args, "-map", strconv.Itoa(i+1)+":a:0")
		if a.Language != "" {
			args = append(args, "-metadata:s:a:"+strconv.Itoa(i), "language="+a.Language)
		}
	}
	return append(args, "-c", "copy", "-f", "matroska", "pipe:1")
}

// subsArgs extracts text subtitle track n as WebVTT, timed against the
// whole file — the page shifts cues itself after a seek.
func subsArgs(src Source, n int) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	args = append(args, inputArgs(src, src.input(), 0)...)
	return append(args, "-map", "0:s:"+strconv.Itoa(n), "-f", "webvtt", "pipe:1")
}

// keyframeArgs asks ffprobe for the timestamp of the first picture
// packet at or before t — where a copied stream that "starts at t"
// really starts, since a copy can only begin on a keyframe.
func keyframeArgs(src Source, t float64) []string {
	args := []string{"-v", "error", "-select_streams", "v:0",
		"-read_intervals", strconv.FormatFloat(t, 'f', 3, 64) + "%+#1",
		"-show_entries", "packet=pts_time", "-of", "csv=p=0"}
	in := inputArgs(src, src.input(), 0)
	args = append(args, in[:len(in)-2]...)
	return append(args, src.input())
}

// cmdError turns a failed ffmpeg/ffprobe/yt-dlp run into its own last
// line of complaint rather than "exit status 1".
func cmdError(err error) error {
	if ee, ok := err.(*exec.ExitError); ok {
		lines := strings.Split(strings.TrimSpace(string(ee.Stderr)), "\n")
		if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
			return fmt.Errorf("%s", last)
		}
	}
	return err
}
