// Package prefs describes every user setting in one place — its key,
// section, label, help, and how to show, parse and validate it — so the
// TUI's Settings tab and "godl settings" list exactly the same options
// and can't drift apart. Values live in store.Settings; this package
// only knows how to present and edit them.
package prefs

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"godl/internal/ratelimit"
	"godl/internal/social"
	"godl/internal/store"
)

type Kind int

const (
	Number Kind = iota
	Text
	Toggle
	Choice
)

// Option is one allowed value of a Choice field.
type Option struct {
	Value string
	Label string
}

// Field is one setting.
type Field struct {
	Key     string // "godl settings set <key>"
	Section string
	Label   string
	Help    string
	Kind    Kind
	Options []Option // Choice only

	// Get returns the stored value as text: what "godl settings set"
	// takes and what an edit box starts from.
	Get func(store.Settings) string
	// Set parses and validates v and stores it in s. An empty v resets
	// Text fields to their default.
	Set func(s *store.Settings, v string) error
	// Unset describes the empty/zero value, e.g. "unlimited".
	Unset string
	// EnvOverride names an environment variable that takes precedence
	// over this setting when set.
	EnvOverride string
}

// Sections lists section names in display order.
var Sections = []string{"Downloads", "Bandwidth", "Video & audio", "Torrents", "Interface"}

// Browsers are the browsers yt-dlp can read cookies from.
var Browsers = []string{"brave", "chrome", "chromium", "edge", "firefox", "opera", "safari", "vivaldi", "whale"}

// Fields lists every setting, grouped by Section in Sections order.
var Fields = []Field{
	{
		Key: "download_dir", Section: "Downloads", Label: "Download folder", Kind: Text,
		Help:        "Where new downloads go when you don't pass -o. Created if missing. Empty = your system Downloads folder.",
		Unset:       "system Downloads folder",
		EnvOverride: "GODL_DOWNLOADS_DIR",
		Get:         func(s store.Settings) string { return s.DownloadDir },
		Set: func(s *store.Settings, v string) error {
			dir, err := normalizeDir(v)
			if err != nil {
				return err
			}
			s.DownloadDir = dir
			return nil
		},
	},
	{
		Key: "max_concurrent", Section: "Downloads", Label: "Max concurrent downloads", Kind: Number,
		Help:  "How many jobs run at once. Extra jobs wait as \"queued\" and start as running ones finish. 0 = unlimited.",
		Unset: "unlimited",
		Get:   func(s store.Settings) string { return strconv.Itoa(s.MaxConcurrent) },
		Set: func(s *store.Settings, v string) error {
			n, err := parseInt(v, 0, 1000)
			s.MaxConcurrent = n
			return err
		},
	},
	{
		Key: "connections", Section: "Downloads", Label: "Connections per download", Kind: Number,
		Help: fmt.Sprintf("How many parts a direct link downloads in parallel (when the server allows it). -c on a single job overrides it. 0 = %d.", store.DefaultConnections),
		Get:  func(s store.Settings) string { return strconv.Itoa(s.EffectiveConnections()) },
		Set: func(s *store.Settings, v string) error {
			n, err := parseInt(v, 0, 32)
			s.Connections = n
			return err
		},
	},
	{
		Key: "auto_retry", Section: "Downloads", Label: "Auto-retry failed downloads", Kind: Toggle,
		Help: "Re-queue a failed job after a short, growing delay (5s, 15s, 45s, ... up to 5 min) instead of leaving it failed.",
		Get:  func(s store.Settings) string { return onOff(s.AutoRetry) },
		Set:  func(s *store.Settings, v string) error { return parseToggle(v, &s.AutoRetry) },
	},
	{
		Key: "auto_retry_max_attempts", Section: "Downloads", Label: "Auto-retry attempts", Kind: Number,
		Help: "How many times to auto-retry before leaving a job failed for good.",
		Get:  func(s store.Settings) string { return strconv.Itoa(s.AutoRetryMaxAttempts) },
		Set: func(s *store.Settings, v string) error {
			n, err := parseInt(v, 1, 100)
			s.AutoRetryMaxAttempts = n
			return err
		},
	},
	{
		Key: "notify_on_complete", Section: "Downloads", Label: "Notify when done", Kind: Toggle,
		Help: "Desktop notification when a download finishes (Linux: notify-send, macOS: built in; not available on Windows).",
		Get:  func(s store.Settings) string { return onOff(s.NotifyOnComplete) },
		Set:  func(s *store.Settings, v string) error { return parseToggle(v, &s.NotifyOnComplete) },
	},
	{
		Key: "default_rate_limit", Section: "Bandwidth", Label: "Speed limit per download", Kind: Text,
		Help:  `e.g. "2M" or "500K". Applies to each job that doesn't pass its own -R, so three jobs can use 3x this together.`,
		Unset: "unlimited",
		Get:   func(s store.Settings) string { return s.DefaultRateLimit },
		Set: func(s *store.Settings, v string) error {
			v, err := parseRate(v)
			s.DefaultRateLimit = v
			return err
		},
	},
	{
		Key: "global_rate_limit", Section: "Bandwidth", Label: "Total speed limit", Kind: Text,
		Help:  `e.g. "5M". Caps all downloads combined. Direct links and WebDAV share it exactly; torrents and videos are each capped at it instead.`,
		Unset: "unlimited",
		Get:   func(s store.Settings) string { return s.GlobalRateLimit },
		Set: func(s *store.Settings, v string) error {
			v, err := parseRate(v)
			s.GlobalRateLimit = v
			return err
		},
	},
	{
		Key: "social_preset", Section: "Video & audio", Label: "Default quality", Kind: Choice,
		Help:    "Quality for video/audio links when you don't pick one (-p/-f, or the preset step in the n wizard).",
		Options: presetOptions(),
		Get: func(s store.Settings) string {
			if s.SocialPreset == "" {
				return "best"
			}
			return s.SocialPreset
		},
		Set: func(s *store.Settings, v string) error {
			v = strings.TrimSpace(v)
			if v == "" || v == "best" {
				s.SocialPreset = ""
				return nil
			}
			if _, ok := social.Lookup(v); !ok {
				return fmt.Errorf("unknown quality %q — one of: %s", v, optionValues(presetOptions()))
			}
			s.SocialPreset = v
			return nil
		},
	},
	{
		Key: "cookies_from_browser", Section: "Video & audio", Label: "Use browser login", Kind: Choice,
		Help:    "Send this browser's cookies to video sites, so login-only, private and age-restricted videos work. --cookies-from-browser overrides it.",
		Options: browserOptions(),
		Get: func(s store.Settings) string {
			if s.CookiesFromBrowser == "" {
				return "off"
			}
			return s.CookiesFromBrowser
		},
		Set: func(s *store.Settings, v string) error {
			v = strings.ToLower(strings.TrimSpace(v))
			if v == "" || v == "off" || v == "none" {
				s.CookiesFromBrowser = ""
				return nil
			}
			for _, b := range Browsers {
				if v == b {
					s.CookiesFromBrowser = v
					return nil
				}
			}
			return fmt.Errorf("unknown browser %q — one of: off, %s", v, strings.Join(Browsers, ", "))
		},
	},
	{
		Key: "seed_ratio", Section: "Torrents", Label: "Keep sharing until ratio", Kind: Text,
		Help:  "After a torrent finishes, keep uploading until you've shared this multiple of its size (e.g. 1.0). --seed-ratio overrides it. Empty/0 = stop at completion.",
		Unset: "off",
		Get: func(s store.Settings) string {
			if s.SeedRatio == 0 {
				return ""
			}
			return strconv.FormatFloat(s.SeedRatio, 'g', -1, 64)
		},
		Set: func(s *store.Settings, v string) error {
			v = strings.TrimSpace(v)
			if v == "" {
				s.SeedRatio = 0
				return nil
			}
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f < 0 || f > 1000 {
				return fmt.Errorf("must be a number like 1 or 1.5 (0 = off)")
			}
			s.SeedRatio = f
			return nil
		},
	},
	{
		Key: "seed_time", Section: "Torrents", Label: "Keep sharing for", Kind: Text,
		Help:  `After a torrent finishes, keep uploading this long, e.g. "30m" or "2h". With a ratio too, whichever comes first. --seed-time overrides it.`,
		Unset: "off",
		Get:   func(s store.Settings) string { return s.SeedTime },
		Set: func(s *store.Settings, v string) error {
			v = strings.TrimSpace(v)
			if v != "" && v != "0" {
				if d, err := time.ParseDuration(v); err != nil || d < 0 {
					return fmt.Errorf(`must be a duration like "30m" or "2h"`)
				}
			} else {
				v = ""
			}
			s.SeedTime = v
			return nil
		},
	},
	{
		Key: "animations", Section: "Interface", Label: "Animations", Kind: Toggle,
		Help:        "Smooth progress bars, spinners and speed graphs in the dashboard. Turn off for slow remote connections.",
		EnvOverride: "GODL_NO_ANIMATION",
		Get:         func(s store.Settings) string { return onOff(!s.NoAnimations) },
		Set: func(s *store.Settings, v string) error {
			on := !s.NoAnimations
			if err := parseToggle(v, &on); err != nil {
				return err
			}
			s.NoAnimations = !on
			return nil
		},
	},
}

// Lookup finds a field by key.
func Lookup(key string) (Field, bool) {
	key = strings.ReplaceAll(strings.ToLower(strings.TrimSpace(key)), "-", "_")
	for _, f := range Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// Display is how a field's current value reads in a list: the Unset
// description in place of an empty value.
func (f Field) Display(s store.Settings) string {
	v := f.Get(s)
	if (v == "" || (f.Kind == Number && v == "0")) && f.Unset != "" {
		return f.Unset
	}
	return v
}

// Overridden reports whether the field's environment variable is set,
// in which case that wins over the saved value.
func (f Field) Overridden() bool {
	return f.EnvOverride != "" && os.Getenv(f.EnvOverride) != ""
}

// Reset puts the field back to its built-in default in s.
func (f Field) Reset(s *store.Settings) {
	f.Set(s, f.Get(store.DefaultSettings()))
}

// Cycle moves a Choice field to the next (dir > 0) or previous option,
// or flips a Toggle.
func (f Field) Cycle(s *store.Settings, dir int) error {
	switch f.Kind {
	case Toggle:
		if f.Get(*s) == "on" {
			return f.Set(s, "off")
		}
		return f.Set(s, "on")
	case Choice:
		cur := f.Get(*s)
		i := 0
		for j, o := range f.Options {
			if o.Value == cur {
				i = j
			}
		}
		n := len(f.Options)
		return f.Set(s, f.Options[((i+dir)%n+n)%n].Value)
	}
	return fmt.Errorf("%s isn't a choice", f.Label)
}

// Validate checks every field of s, returning the first problem.
func Validate(s store.Settings) error {
	for _, f := range Fields {
		probe := s
		if err := f.Set(&probe, f.Get(s)); err != nil {
			return fmt.Errorf("%s: %w", f.Label, err)
		}
	}
	return nil
}

func presetOptions() []Option {
	out := make([]Option, len(social.Presets))
	for i, p := range social.Presets {
		out[i] = Option{Value: p.Name, Label: p.Description}
	}
	return out
}

func browserOptions() []Option {
	out := []Option{{Value: "off", Label: "don't send browser cookies"}}
	for _, b := range Browsers {
		out = append(out, Option{Value: b, Label: "use " + b + "'s cookies"})
	}
	return out
}

func optionValues(opts []Option) string {
	vals := make([]string, len(opts))
	for i, o := range opts {
		vals[i] = o.Value
	}
	return strings.Join(vals, ", ")
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func parseToggle(v string, dst *bool) error {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "yes", "1":
		*dst = true
	case "off", "false", "no", "0":
		*dst = false
	default:
		return fmt.Errorf(`must be "on" or "off"`)
	}
	return nil
}

func parseInt(v string, lo, hi int) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("must be a whole number from %d to %d", lo, hi)
	}
	return n, nil
}

func parseRate(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" || v == "0" {
		return "", nil
	}
	if _, err := ratelimit.ParseRate(v); err != nil {
		return "", err
	}
	return v, nil
}

// normalizeDir expands a leading ~ and makes the path absolute, so the
// daemon (with its own working directory) reads it the same way.
func normalizeDir(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", nil
	}
	if v == "~" || strings.HasPrefix(v, "~/") || strings.HasPrefix(v, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		v = filepath.Join(home, v[1:])
	}
	return filepath.Abs(v)
}
