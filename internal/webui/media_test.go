package webui

import (
	"net/http"
	"slices"
	"strings"
	"testing"
)

func idx(args []string, flag string) int { return slices.Index(args, flag) }

func TestStreamArgsCopiesVideoAndPicksAudio(t *testing.T) {
	info := Info{
		Video: &Video{Codec: "h264"},
		Audio: []Track{{Index: 0, Codec: "aac"}, {Index: 1, Codec: "ac3"}},
	}
	src := Source{Name: "film.mkv", Path: "/films/film.mkv"}

	args := streamArgs(src, info, 1, 0)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-map 0:v:0 -c:v copy") {
		t.Errorf("video must always be copied: %s", joined)
	}
	if strings.Contains(joined, "libx26") || strings.Contains(joined, "-c:v lib") {
		t.Errorf("video must never be re-encoded: %s", joined)
	}
	if !strings.Contains(joined, "-map 0:a:1 -c:a aac") {
		t.Errorf("AC-3 track 1 should be selected and converted to AAC: %s", joined)
	}
	if idx(args, "-ss") >= 0 {
		t.Errorf("no seek asked for, but got -ss: %s", joined)
	}
	if args[len(args)-1] != "pipe:1" || !strings.Contains(joined, "frag_keyframe+empty_moov") {
		t.Errorf("output must be fragmented MP4 on stdout: %s", joined)
	}

	args = streamArgs(src, info, 0, 61.5)
	joined = strings.Join(args, " ")
	if !strings.Contains(joined, "-map 0:a:0 -c:a copy") {
		t.Errorf("an AAC track should be copied: %s", joined)
	}
	if i := idx(args, "-ss"); i < 0 || args[i+1] != "61.500" || i > idx(args, "-i") {
		t.Errorf("seek must come before -i (a fast keyframe seek): %s", joined)
	}

	// An out-of-range track falls back to the first rather than failing.
	if joined := strings.Join(streamArgs(src, info, 9, 0), " "); !strings.Contains(joined, "-map 0:a:0") {
		t.Errorf("out-of-range audio should fall back to track 0: %s", joined)
	}

	info.Video.Codec = "hevc"
	if joined := strings.Join(streamArgs(src, info, 0, 0), " "); !strings.Contains(joined, "-tag:v hvc1") {
		t.Errorf("HEVC needs the hvc1 tag to play in browsers: %s", joined)
	}
}

func TestStreamArgsForALinkWithSeparateAudio(t *testing.T) {
	src := Source{
		Name: "talk", URL: "https://cdn/video", UserAgent: "UA",
		Audio: []AudioInput{{URL: "https://cdn/en", Codec: "mp4a.40.2"}, {URL: "https://cdn/hi", Codec: "opus"}},
	}
	info := Info{Video: &Video{Codec: "h264"}, Audio: []Track{{Index: 0}, {Index: 1}}}
	args := streamArgs(src, info, 1, 30)
	joined := strings.Join(args, " ")
	if strings.Count(joined, "-ss 30.000") != 2 {
		t.Errorf("both inputs must seek: %s", joined)
	}
	if !strings.Contains(joined, "-i https://cdn/hi") || strings.Contains(joined, "https://cdn/en") {
		t.Errorf("only the chosen language's audio should be opened: %s", joined)
	}
	if !strings.Contains(joined, "-map 1:a:0 -c:a aac") {
		t.Errorf("opus from the second input should be converted: %s", joined)
	}
	if !strings.Contains(joined, "-user_agent UA") {
		t.Errorf("the site's user agent should be sent: %s", joined)
	}
}

func TestInputArgsSendsHeadersOnlyForURLs(t *testing.T) {
	src := Source{URL: "https://dav/x.mkv", Header: http.Header{"Authorization": {"Basic abc"}}}
	joined := strings.Join(inputArgs(src, src.URL, 0), " ")
	if !strings.Contains(joined, "-headers Authorization: Basic abc\r\n") {
		t.Errorf("WebDAV credentials should go in -headers: %q", joined)
	}
	if strings.Contains(strings.Join(inputArgs(Source{Path: "/a"}, "/a", 0), " "), "-reconnect") {
		t.Error("a local file needs no HTTP options")
	}
}

func TestInfoFromProbe(t *testing.T) {
	var p ffprobeOutput
	p.Format.Duration = "125.5"
	p.Streams = append(p.Streams,
		struct {
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
		}{CodecType: "video", CodecName: "h264", Profile: "High", Level: 40, Width: 1920, Height: 1080, PixFmt: "yuv420p"},
	)
	p.Streams = append(p.Streams, p.Streams[0])
	p.Streams[1].CodecType, p.Streams[1].CodecName, p.Streams[1].Tags = "audio", "eac3", map[string]string{"language": "hin"}
	p.Streams = append(p.Streams, p.Streams[0])
	p.Streams[2].CodecType, p.Streams[2].CodecName = "subtitle", "hdmv_pgs_subtitle"

	info := infoFromProbe("film.mkv", p)
	if info.Duration != 125.5 || info.Video == nil || info.Video.MIME != `video/mp4; codecs="avc1.640028"` {
		t.Errorf("video info = %+v %+v", info, info.Video)
	}
	if len(info.Audio) != 1 || info.Audio[0].Language != "hin" {
		t.Errorf("audio = %+v", info.Audio)
	}
	if len(info.Subs) != 1 || info.Subs[0].Text {
		t.Errorf("a PGS track must be marked as not text: %+v", info.Subs)
	}
	if info.Direct {
		t.Error("an MKV is never played direct")
	}

	empty := infoFromProbe("a.mp4", ffprobeOutput{})
	if empty.Audio == nil || empty.Subs == nil {
		t.Error("no tracks must be empty lists, not null (the page iterates them)")
	}
}

func TestDirectPlayable(t *testing.T) {
	cases := []struct {
		name  string
		audio []Track
		want  bool
	}{
		{"a.mp4", []Track{{Codec: "aac"}}, true},
		{"a.MP4", nil, true},
		{"a.webm", []Track{{Codec: "opus"}}, true},
		{"a.mp4", []Track{{Codec: "aac"}, {Codec: "aac"}}, false}, // a choice of language
		{"a.mp4", []Track{{Codec: "ac3"}}, false},                 // no browser plays AC-3
		{"a.mkv", []Track{{Codec: "aac"}}, false},
	}
	for _, c := range cases {
		if got := directPlayable(c.name, Info{Audio: c.audio}); got != c.want {
			t.Errorf("directPlayable(%s, %v) = %v, want %v", c.name, c.audio, got, c.want)
		}
	}
}

func TestVideoMIME(t *testing.T) {
	cases := map[string]string{
		videoMIME("h264", "Main", 31, "yuv420p"):         `video/mp4; codecs="avc1.4d001f"`,
		videoMIME("hevc", "Main 10", 120, "yuv420p10le"): `video/mp4; codecs="hvc1.2.4.L120.B0"`,
		videoMIME("vp9", "", 0, "yuv420p"):               `video/mp4; codecs="vp09.00.10.08"`,
		videoMIME("mpeg4", "", 0, "yuv420p"):             "",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("videoMIME = %q, want %q", got, want)
		}
	}
}
