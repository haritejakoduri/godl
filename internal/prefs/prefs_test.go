package prefs

import (
	"os"
	"path/filepath"
	"testing"

	"godl/internal/store"
)

// Every field of the zero Settings and of DefaultSettings must be valid,
// or a fresh install (or an older database missing the newer keys) would
// be refused on its first save.
func TestDefaultsValidate(t *testing.T) {
	if err := Validate(store.DefaultSettings()); err != nil {
		t.Errorf("DefaultSettings: %v", err)
	}
	zero := store.Settings{AutoRetryMaxAttempts: 1}
	if err := Validate(zero); err != nil {
		t.Errorf("zero settings: %v", err)
	}
}

func TestFieldsAreUniqueAndGroupedInSectionOrder(t *testing.T) {
	seen := map[string]bool{}
	order := map[string]int{}
	for i, s := range Sections {
		order[s] = i
	}
	last := 0
	for _, f := range Fields {
		if seen[f.Key] {
			t.Errorf("duplicate key %q", f.Key)
		}
		seen[f.Key] = true
		idx, ok := order[f.Section]
		if !ok {
			t.Errorf("%s: unknown section %q", f.Key, f.Section)
		}
		if idx < last {
			t.Errorf("%s: section %q appears after a later section — fields must be grouped", f.Key, f.Section)
		}
		last = idx
		if f.Label == "" || f.Help == "" {
			t.Errorf("%s: missing label or help", f.Key)
		}
	}
}

// Whatever a field shows as its value must be accepted back by Set:
// that's what the TUI's edit box starts from and what "godl settings
// get | set" round-trips.
func TestGetSetRoundTrip(t *testing.T) {
	s := store.Settings{
		MaxConcurrent: 2, AutoRetryMaxAttempts: 4, DefaultRateLimit: "2M", GlobalRateLimit: "10M",
		DownloadDir: "/srv/dl", Connections: 8, SocialPreset: "720p", CookiesFromBrowser: "firefox",
		SeedRatio: 1.5, SeedTime: "2h", AutoRetry: true, NoAnimations: true,
	}
	for _, f := range Fields {
		got := s
		if err := f.Set(&got, f.Get(s)); err != nil {
			t.Errorf("%s: Set(Get()) = %v", f.Key, err)
			continue
		}
		if f.Get(got) != f.Get(s) {
			t.Errorf("%s: round trip %q -> %q", f.Key, f.Get(s), f.Get(got))
		}
	}
}

func TestSetRejectsBadValues(t *testing.T) {
	bad := map[string]string{
		"max_concurrent":          "-1",
		"connections":             "99",
		"auto_retry_max_attempts": "0",
		"default_rate_limit":      "fast",
		"social_preset":           "4k",
		"cookies_from_browser":    "netscape",
		"seed_ratio":              "lots",
		"seed_time":               "tomorrow",
		"auto_retry":              "maybe",
	}
	for k, v := range bad {
		f, ok := Lookup(k)
		if !ok {
			t.Fatalf("no field %q", k)
		}
		s := store.DefaultSettings()
		if err := f.Set(&s, v); err == nil {
			t.Errorf("%s accepted %q", k, v)
		}
	}
}

func TestDownloadDirExpandsHomeAndMakesAbsolute(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	f, _ := Lookup("download-dir") // dashes accepted too
	s := store.DefaultSettings()
	if err := f.Set(&s, "~/Media"); err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "Media"); s.DownloadDir != want {
		t.Errorf("DownloadDir = %q, want %q", s.DownloadDir, want)
	}
	if err := f.Set(&s, ""); err != nil || s.DownloadDir != "" {
		t.Errorf("empty didn't reset to the system folder: %q, %v", s.DownloadDir, err)
	}
}

func TestResetRestoresDefaults(t *testing.T) {
	s := store.Settings{Connections: 16, SocialPreset: "audio", NoAnimations: true, AutoRetryMaxAttempts: 9}
	for _, k := range []string{"connections", "social_preset", "animations", "auto_retry_max_attempts"} {
		f, _ := Lookup(k)
		f.Reset(&s)
	}
	def := store.DefaultSettings()
	if s.EffectiveConnections() != def.EffectiveConnections() || s.SocialPreset != "" || s.NoAnimations || s.AutoRetryMaxAttempts != def.AutoRetryMaxAttempts {
		t.Errorf("after reset: %+v", s)
	}
}

func TestOverriddenFollowsTheEnvironment(t *testing.T) {
	f, _ := Lookup("download_dir")
	t.Setenv("GODL_DOWNLOADS_DIR", "")
	if f.Overridden() {
		t.Error("overridden with the variable empty")
	}
	t.Setenv("GODL_DOWNLOADS_DIR", "/tmp/x")
	if !f.Overridden() {
		t.Error("not overridden with GODL_DOWNLOADS_DIR set")
	}
}
