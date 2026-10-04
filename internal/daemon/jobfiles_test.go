package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"godl/internal/store"
)

func TestJobFileListFromDisk(t *testing.T) {
	d := newTestDaemon(t)
	ctx := context.Background()
	out := t.TempDir()
	write := func(rel string, n int) string {
		p := filepath.Join(out, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// A finished WebDAV folder job: every file it saved, folder name kept.
	a := write("Album/one.flac", 10)
	b := write("Album/disc2/two.flac", 20)
	write("Album/two.flac.godl-progress.json", 5) // bookkeeping, not a file of the job
	j := &store.Job{ID: "w1", Type: store.JobWebDAV, Source: "nas:/Music/Album", Output: out, Status: store.StatusCompleted}
	d.st.CreateJob(ctx, j)
	d.st.AppendResolvedPath(ctx, "w1", a)
	d.st.AppendResolvedPath(ctx, "w1", b)

	files, note, err := d.jobFileList("w1")
	if err != nil {
		t.Fatal(err)
	}
	if note != "" || len(files) != 2 {
		t.Fatalf("files=%+v note=%q, want the two saved files and no note", files, note)
	}
	if files[0].Path != "Album/disc2/two.flac" || files[0].Done != 20 || files[0].Length != 20 {
		t.Errorf("first file = %+v", files[0])
	}

	// Paused: lists what's in the folder so far, and says it's partial.
	write("Album/three.flac", 3)
	j, _ = d.st.GetJob(ctx, "w1") // with the saved-file list
	j.Status = store.StatusPaused
	d.st.UpdateJob(ctx, j)
	files, note, _ = d.jobFileList("w1")
	if len(files) != 3 || !strings.Contains(note, "so far") {
		t.Errorf("paused job: %d files, note %q; want 3 files and a note that it's partial", len(files), note)
	}
	for _, f := range files {
		finishedFile := f.Path != "Album/three.flac"
		if finishedFile != (f.Length == f.Done && f.Length > 0) {
			t.Errorf("paused job file %+v: only files the job finished should count as complete", f)
		}
	}

	// A torrent that never got its metadata: no files, and why.
	d.st.CreateJob(ctx, &store.Job{ID: "t1", Type: store.JobTorrent, Source: "magnet:?xt=urn:btih:abc", Output: out, Status: store.StatusPaused})
	files, note, _ = d.jobFileList("t1")
	if len(files) != 0 || note == "" {
		t.Errorf("torrent without metadata: files=%+v note=%q", files, note)
	}
	if files == nil {
		t.Error("an empty list must be [], not null")
	}

	if _, _, err := d.jobFileList("nope"); err == nil {
		t.Error("unknown job: want an error")
	}
}

func TestWebDAVDisplayPath(t *testing.T) {
	cases := []struct {
		root, file string
		dir        bool
		want       string
	}{
		{"/Music/Album", "/Music/Album/disc2/two.flac", true, "Album/disc2/two.flac"},
		{"/Music/Album/", "/Music/Album/one.flac", true, "Album/one.flac"},
		{"/", "/top.txt", true, "top.txt"},
		{"/Music/song.mp3", "/Music/song.mp3", false, "song.mp3"},
	}
	for _, c := range cases {
		if got := webdavDisplayPath(c.root, c.file, c.dir); got != c.want {
			t.Errorf("webdavDisplayPath(%q, %q, %v) = %q, want %q", c.root, c.file, c.dir, got, c.want)
		}
	}
}
