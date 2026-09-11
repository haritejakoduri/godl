// Package social holds the quality presets offered for yt-dlp-backed
// downloads.
//
// They live here rather than beside the cobra command because both front
// ends need them: "godl social --quality" validates against this list,
// and the TUI's new-download wizard presents it as a menu. A GUI would
// be a third caller.
package social

// Preset is one named yt-dlp format selection. Format is passed to
// yt-dlp's -f flag verbatim; empty means "don't pass -f at all", which
// is how yt-dlp's own default behavior is requested.
type Preset struct {
	Name        string
	Format      string
	Description string
}

// Presets is the offered list, in the order a menu should show it:
// best first, then descending caps, then the two special cases.
var Presets = []Preset{
	{"best", "", "Best combined quality (yt-dlp's default)"},
	{"1080p", "bv*[height<=1080]+ba/b[height<=1080]", "Cap at 1080p, best audio"},
	{"720p", "bv*[height<=720]+ba/b[height<=720]", "Cap at 720p, best audio"},
	{"480p", "bv*[height<=480]+ba/b[height<=480]", "Cap at 480p, best audio"},
	{"worst", "worst", "Lowest quality (quick preview/test)"},
	{"audio", "bestaudio/best", "Audio only, best available quality"},
}

// Lookup finds a preset by name. ok is false for an unknown name, so
// callers can report the valid set rather than silently downloading at
// some other quality than the one that was asked for.
func Lookup(name string) (Preset, bool) {
	for _, p := range Presets {
		if p.Name == name {
			return p, true
		}
	}
	return Preset{}, false
}
