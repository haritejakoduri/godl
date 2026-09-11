package cmd

import "testing"

func TestSha256PatternValidatesDigestFormat(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"valid lowercase", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", true},
		{"valid uppercase", "E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855", true},
		{"too short", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b85", false},
		{"too long", "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b8555", false},
		{"non-hex chars", "g3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sha256Pattern.MatchString(c.in); got != c.want {
				t.Errorf("sha256Pattern.MatchString(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}
