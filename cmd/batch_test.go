package cmd

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadLinksSkipsBlanksAndComments(t *testing.T) {
	got, err := readLinks(strings.NewReader("  https://a.example/1  \n\n# a comment\nhttps://b.example/2\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"https://a.example/1", "https://b.example/2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("readLinks = %q, want %q", got, want)
	}
}

func TestUniqueNameAvoidsCollisionsWithinABatch(t *testing.T) {
	taken := map[string]bool{}
	got := []string{
		uniqueName("file.iso", taken),
		uniqueName("file.iso", taken),
		uniqueName("file.iso", taken),
		uniqueName("README", taken),
		uniqueName("README", taken),
	}
	want := []string{"file.iso", "file (2).iso", "file (3).iso", "README", "README (2)"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("uniqueName sequence = %q, want %q", got, want)
	}
}

// With several links, -o names the directory every file goes into.
func TestURLOutputsTreatOutputAsDirectoryForBatches(t *testing.T) {
	dir := t.TempDir()
	outs, err := urlOutputs([]string{"https://x/a.zip", "https://y/a.zip"}, dir, func(link string) string {
		return filepath.Base(link)
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(dir, "a.zip"), filepath.Join(dir, "a (2).zip")}
	if !reflect.DeepEqual(outs, want) {
		t.Errorf("urlOutputs = %q, want %q", outs, want)
	}
}
