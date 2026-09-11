package cmd

import "testing"

func TestPrintSocialPresets(t *testing.T) {
	if err := printSocialPresets(); err != nil {
		t.Fatal(err)
	}
}
