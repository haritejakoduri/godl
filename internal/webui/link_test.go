package webui

import "testing"

func TestSourceFromYtdlpPicksVideoAndAudioPerLanguage(t *testing.T) {
	y := ytdlpInfo{
		Title: "Talk", Duration: 600,
		Formats: []ytdlpFormat{
			{URL: "v1080avc", VCodec: "avc1.640028", ACodec: "none", Height: 1080, Protocol: "https"},
			{URL: "v720avc", VCodec: "avc1.4d401f", ACodec: "none", Height: 720, Protocol: "https"},
			{URL: "v720vp9", VCodec: "vp9", ACodec: "none", Height: 720, Protocol: "https", TBR: 9999},
			{URL: "v720hls", VCodec: "avc1.4d401f", ACodec: "none", Height: 720, Protocol: "m3u8_native", TBR: 9999},
			{URL: "en-opus", VCodec: "none", ACodec: "opus", ABR: 160, Language: "en", LanguagePreference: 10, Protocol: "https"},
			{URL: "en-aac", VCodec: "none", ACodec: "mp4a.40.2", ABR: 128, Language: "en", LanguagePreference: 10, Protocol: "https"},
			{URL: "hi-aac", VCodec: "none", ACodec: "mp4a.40.2", ABR: 128, Language: "hi", Protocol: "https"},
			{URL: "both360", VCodec: "avc1.42001e", ACodec: "mp4a.40.2", Height: 360, Protocol: "https"},
		},
		Subtitles: map[string][]ytdlpSub{"en": {{Ext: "json3", URL: "x"}, {Ext: "vtt", URL: "en.vtt"}}},
	}
	src, info, err := sourceFromYtdlp(y, 720)
	if err != nil {
		t.Fatal(err)
	}
	if src.URL != "v720avc" {
		t.Errorf("picture = %s, want the 720p H.264 over HTTP (not VP9, not HLS, not 1080p)", src.URL)
	}
	if len(src.Audio) != 2 || src.Audio[0].URL != "en-aac" || src.Audio[1].URL != "hi-aac" {
		t.Errorf("audio = %+v, want AAC per language, original language first", src.Audio)
	}
	if len(info.Audio) != 2 || info.Audio[1].Language != "hi" {
		t.Errorf("info audio = %+v", info.Audio)
	}
	if len(src.Subs) != 1 || src.Subs[0].URL != "en.vtt" || !info.Subs[0].Text {
		t.Errorf("subs = %+v", src.Subs)
	}
	if !src.Remote || info.Duration != 600 || info.Video.Codec != "h264" {
		t.Errorf("src/info = %+v %+v", src, info)
	}
}

func TestSourceFromYtdlpCombinedOnly(t *testing.T) {
	y := ytdlpInfo{Title: "Clip", Formats: []ytdlpFormat{
		{URL: "low", VCodec: "avc1", ACodec: "mp4a.40.2", Height: 240, Protocol: "https"},
		{URL: "high", VCodec: "avc1", ACodec: "mp4a.40.2", Height: 480, Protocol: "https"},
	}}
	src, info, err := sourceFromYtdlp(y, 0)
	if err != nil {
		t.Fatal(err)
	}
	if src.URL != "high" || len(src.Audio) != 0 || len(info.Audio) != 1 {
		t.Errorf("combined-only site: src=%+v info=%+v", src, info)
	}
}

func TestSourceFromYtdlpNothing(t *testing.T) {
	if _, _, err := sourceFromYtdlp(ytdlpInfo{}, 0); err == nil {
		t.Error("no formats should be an error")
	}
}
