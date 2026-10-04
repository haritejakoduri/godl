package webui

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// ytdlpInfo is the slice of "yt-dlp -J" this package reads.
type ytdlpInfo struct {
	Title     string                `json:"title"`
	Duration  float64               `json:"duration"`
	Formats   []ytdlpFormat         `json:"formats"`
	Subtitles map[string][]ytdlpSub `json:"subtitles"`
}

type ytdlpFormat struct {
	URL      string  `json:"url"`
	Ext      string  `json:"ext"`
	VCodec   string  `json:"vcodec"`
	ACodec   string  `json:"acodec"`
	Height   int     `json:"height"`
	Width    int     `json:"width"`
	TBR      float64 `json:"tbr"`
	ABR      float64 `json:"abr"`
	Protocol string  `json:"protocol"`
	Language string  `json:"language"`
	// 10 on the track the video was made in, lower on dubs.
	LanguagePreference int               `json:"language_preference"`
	FormatNote         string            `json:"format_note"`
	HTTPHeaders        map[string]string `json:"http_headers"`
	// Set by yt-dlp on streams the site slows down when read in one
	// long request (YouTube's): fetch them this many bytes at a time.
	DownloaderOptions struct {
		HTTPChunkSize int64 `json:"http_chunk_size"`
	} `json:"downloader_options"`
}

type ytdlpSub struct {
	Ext  string `json:"ext"`
	URL  string `json:"url"`
	Name string `json:"name"`
}

// resolveLink asks yt-dlp what a web link (a YouTube watch page, say)
// can be played from, and picks the streams the page's player will use:
// the best picture up to maxHeight, and one audio stream per language.
func resolveLink(ctx context.Context, ytDlpPath, link string, maxHeight int, extra []string) (Source, Info, error) {
	args := append([]string{"-J", "--no-playlist", "--no-warnings"}, extra...)
	out, err := exec.CommandContext(ctx, ytDlpPath, append(args, link)...).Output()
	if err != nil {
		return Source{}, Info{}, cmdError(err)
	}
	var y ytdlpInfo
	if err := json.Unmarshal(out, &y); err != nil {
		return Source{}, Info{}, fmt.Errorf("reading yt-dlp's answer: %w", err)
	}
	return sourceFromYtdlp(y, maxHeight)
}

func has(codec string) bool { return codec != "" && codec != "none" }

// plainHTTP is a format ffmpeg can seek in over HTTP. HLS playlists
// work too, but worse, so they're only a fallback.
func plainHTTP(f ytdlpFormat) bool { return f.Protocol == "https" || f.Protocol == "http" }

func sourceFromYtdlp(y ytdlpInfo, maxHeight int) (Source, Info, error) {
	name := y.Title
	if name == "" {
		name = "video"
	}
	src := Source{Name: name, Remote: true}
	info := Info{Name: name, Duration: y.Duration}

	video, split := pickVideo(y.Formats, maxHeight)
	if video == nil {
		// Audio-only pages (a podcast, a music track).
		audios := pickAudio(y.Formats)
		if len(audios) == 0 {
			return Source{}, Info{}, fmt.Errorf("yt-dlp found nothing playable at that link")
		}
		first := audios[0]
		src.URL, src.UserAgent = first.URL, first.HTTPHeaders["User-Agent"]
		src.ChunkSize = first.DownloaderOptions.HTTPChunkSize
		info.Audio = []Track{{Index: 0, Language: first.Language, Title: first.FormatNote, Codec: first.ACodec}}
		return src, info.withEmptyLists(), nil
	}

	src.URL, src.UserAgent = video.URL, video.HTTPHeaders["User-Agent"]
	src.ChunkSize = video.DownloaderOptions.HTTPChunkSize
	codec := ffmpegCodecName(video.VCodec)
	info.Video = &Video{Codec: codec, Width: video.Width, Height: video.Height, MIME: videoMIMEFromTag(video.VCodec)}

	if split {
		for i, a := range pickAudio(y.Formats) {
			src.Audio = append(src.Audio, AudioInput{URL: a.URL, Language: a.Language, Title: a.FormatNote, Codec: a.ACodec})
			info.Audio = append(info.Audio, Track{Index: i, Language: a.Language, Title: a.FormatNote, Codec: a.ACodec})
		}
	}
	if len(src.Audio) == 0 && has(video.ACodec) {
		info.Audio = []Track{{Index: 0, Language: video.Language, Codec: video.ACodec}}
	}

	langs := make([]string, 0, len(y.Subtitles))
	for lang := range y.Subtitles {
		langs = append(langs, lang)
	}
	sort.Strings(langs)
	for _, lang := range langs {
		for _, s := range y.Subtitles[lang] {
			if s.Ext != "vtt" || s.URL == "" {
				continue
			}
			src.Subs = append(src.Subs, SubInput{URL: s.URL, Language: lang, Title: s.Name})
			info.Subs = append(info.Subs, Track{Index: len(info.Subs), Language: lang, Title: s.Name, Codec: "webvtt", Text: true})
			break
		}
	}
	return src, info.withEmptyLists(), nil
}

// pickVideo chooses the picture stream: the tallest at or under
// maxHeight, H.264 over anything else at the same height (every
// browser decodes it), then the higher bitrate. split reports whether
// it's picture-only, i.e. the sound comes from pickAudio.
func pickVideo(formats []ytdlpFormat, maxHeight int) (best *ytdlpFormat, split bool) {
	better := func(a, b *ytdlpFormat) bool { // is a better than b
		if plainHTTP(*a) != plainHTTP(*b) {
			return plainHTTP(*a)
		}
		if a.Height != b.Height {
			return a.Height > b.Height
		}
		if ah, bh := strings.HasPrefix(a.VCodec, "avc"), strings.HasPrefix(b.VCodec, "avc"); ah != bh {
			return ah
		}
		return a.TBR > b.TBR
	}
	pick := func(want func(ytdlpFormat) bool) *ytdlpFormat {
		var best *ytdlpFormat
		for i := range formats {
			f := &formats[i]
			if f.URL == "" || !has(f.VCodec) || !want(*f) {
				continue
			}
			if maxHeight > 0 && f.Height > maxHeight {
				continue
			}
			if best == nil || better(f, best) {
				best = f
			}
		}
		return best
	}

	hasSeparateAudio := len(pickAudio(formats)) > 0
	if hasSeparateAudio {
		if v := pick(func(f ytdlpFormat) bool { return !has(f.ACodec) && plainHTTP(f) }); v != nil {
			return v, true
		}
	}
	if v := pick(func(f ytdlpFormat) bool { return has(f.ACodec) }); v != nil {
		return v, false
	}
	if hasSeparateAudio {
		if v := pick(func(f ytdlpFormat) bool { return !has(f.ACodec) }); v != nil {
			return v, true
		}
	}
	return nil, false
}

// pickAudio returns one audio-only stream per language — AAC where the
// site offers it (it's copied rather than converted), then the higher
// bitrate — with the video's original language first.
func pickAudio(formats []ytdlpFormat) []ytdlpFormat {
	best := map[string]ytdlpFormat{}
	for _, f := range formats {
		if f.URL == "" || has(f.VCodec) || !has(f.ACodec) || !plainHTTP(f) {
			continue
		}
		cur, ok := best[f.Language]
		if !ok || audioBetter(f, cur) {
			best[f.Language] = f
		}
	}
	out := make([]ytdlpFormat, 0, len(best))
	for _, f := range best {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LanguagePreference != out[j].LanguagePreference {
			return out[i].LanguagePreference > out[j].LanguagePreference
		}
		return out[i].Language < out[j].Language
	})
	return out
}

func audioBetter(a, b ytdlpFormat) bool {
	if isAAC(a.ACodec) != isAAC(b.ACodec) {
		return isAAC(a.ACodec)
	}
	return a.ABR > b.ABR
}

// ffmpegCodecName maps yt-dlp's codec tag ("avc1.640028") to the name
// ffmpeg uses for it ("h264").
func ffmpegCodecName(tag string) string {
	switch {
	case strings.HasPrefix(tag, "avc"):
		return "h264"
	case strings.HasPrefix(tag, "hev"), strings.HasPrefix(tag, "hvc"):
		return "hevc"
	case strings.HasPrefix(tag, "vp09"), strings.HasPrefix(tag, "vp9"):
		return "vp9"
	case strings.HasPrefix(tag, "av01"):
		return "av1"
	}
	return tag
}

// videoMIMEFromTag is videoMIME for a site that already states the
// exact codec string.
func videoMIMEFromTag(tag string) string {
	switch ffmpegCodecName(tag) {
	case "h264", "av1":
		return `video/mp4; codecs="` + tag + `"`
	case "hevc":
		return `video/mp4; codecs="` + strings.Replace(tag, "hev1", "hvc1", 1) + `"`
	case "vp9":
		if strings.HasPrefix(tag, "vp09") {
			return `video/mp4; codecs="` + tag + `"`
		}
		return `video/mp4; codecs="vp09.00.10.08"`
	}
	return ""
}
