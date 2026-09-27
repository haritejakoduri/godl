package torrentmgr

import "testing"

func TestSelectionMatchesNumbersRangesAndGlobs(t *testing.T) {
	sel, err := ParseSelection("1, 4-5, *.mkv, Extras/*")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		index int
		path  string
		want  bool
	}{
		{1, "readme.txt", true},
		{2, "sample.txt", false},
		{4, "a.txt", true},
		{5, "b.txt", true},
		{6, "c.txt", false},
		{9, "Season 1/E01.mkv", true}, // base-name match
		{10, "Extras/interview.mp4", true},
		{11, "Other/interview.mp4", false},
	}
	for _, c := range cases {
		if got := sel.Match(c.index, c.path); got != c.want {
			t.Errorf("Match(%d, %q) = %v, want %v", c.index, c.path, got, c.want)
		}
	}
}

func TestEmptySelectionSelectsEverything(t *testing.T) {
	sel, err := ParseSelection("  ")
	if err != nil || sel != nil {
		t.Fatalf("ParseSelection(blank) = %v, %v; want nil, nil", sel, err)
	}
	if !sel.Match(7, "anything") {
		t.Error("a nil selection must match every file")
	}
}

func TestSelectionRejectsInvalidRanges(t *testing.T) {
	for _, bad := range []string{"0", "5-2", "["} {
		if _, err := ParseSelection(bad); err == nil {
			t.Errorf("ParseSelection(%q) accepted an invalid selection", bad)
		}
	}
}
