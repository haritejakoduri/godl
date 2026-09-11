package social

import "testing"

func TestLookupSocialPreset(t *testing.T) {
	p, ok := Lookup("720p")
	if !ok {
		t.Fatal("Lookup(720p) not found")
	}
	if p.Format == "" {
		t.Error("720p preset should carry a non-empty format selector")
	}

	best, ok := Lookup("best")
	if !ok {
		t.Fatal("Lookup(best) not found")
	}
	if best.Format != "" {
		t.Errorf("best preset Format = %q, want empty (no -f passed, yt-dlp's own default)", best.Format)
	}

	if _, ok := Lookup("does-not-exist"); ok {
		t.Error("lookupSocialPreset should report false for an unknown name")
	}
}

func TestSocialPresetNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Presets {
		if seen[p.Name] {
			t.Errorf("duplicate preset name %q", p.Name)
		}
		seen[p.Name] = true
		if p.Description == "" {
			t.Errorf("preset %q has no description", p.Name)
		}
	}
}
