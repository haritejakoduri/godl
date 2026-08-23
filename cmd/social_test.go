package cmd

import "testing"

func TestLookupSocialPreset(t *testing.T) {
	p, ok := lookupSocialPreset("720p")
	if !ok {
		t.Fatal("lookupSocialPreset(720p) not found")
	}
	if p.Format == "" {
		t.Error("720p preset should carry a non-empty format selector")
	}

	best, ok := lookupSocialPreset("best")
	if !ok {
		t.Fatal("lookupSocialPreset(best) not found")
	}
	if best.Format != "" {
		t.Errorf("best preset Format = %q, want empty (no -f passed, yt-dlp's own default)", best.Format)
	}

	if _, ok := lookupSocialPreset("does-not-exist"); ok {
		t.Error("lookupSocialPreset should report false for an unknown name")
	}
}

func TestSocialPresetNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range socialPresets {
		if seen[p.Name] {
			t.Errorf("duplicate preset name %q", p.Name)
		}
		seen[p.Name] = true
		if p.Description == "" {
			t.Errorf("preset %q has no description", p.Name)
		}
	}
}

func TestPrintSocialPresets(t *testing.T) {
	if err := printSocialPresets(); err != nil {
		t.Fatal(err)
	}
}
