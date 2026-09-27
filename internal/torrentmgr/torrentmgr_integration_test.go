package torrentmgr

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

// loopbackConfig is a normal client config confined to loopback with
// no DHT, trackers or port mapping. Not torrent.TestingConfig: that caps
// per-connection request data at a few bytes for anacrolix's own tests,
// which slows a real transfer to a crawl.
func loopbackConfig(t *testing.T) *torrent.ClientConfig {
	cfg := torrent.NewDefaultClientConfig()
	cfg.ListenHost = torrent.LoopbackListenHost
	cfg.ListenPort = 0
	cfg.NoDHT = true
	cfg.DisableTrackers = true
	cfg.NoDefaultPortForwarding = true
	cfg.DisableAcceptRateLimiting = true
	cfg.DisableIPv6 = true // some CI hosts have no IPv6
	cfg.DataDir = t.TempDir()
	return cfg
}

// seedLocally builds a three-file torrent and seeds it from a loopback
// client, returning the .torrent path, the seeder, and each file's
// content by name.
func seedLocally(t *testing.T) (string, *torrent.Client, map[string][]byte) {
	t.Helper()
	src := t.TempDir()
	content := map[string][]byte{
		"episode.mkv": make([]byte, 2<<20),
		"notes.txt":   make([]byte, 300<<10),
		"info.nfo":    make([]byte, 100<<10),
	}
	if err := os.MkdirAll(filepath.Join(src, "Show"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range content {
		rand.Read(data)
		if err := os.WriteFile(filepath.Join(src, "Show", name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	info := metainfo.Info{PieceLength: 32 << 10}
	if err := info.BuildFromFilePath(filepath.Join(src, "Show")); err != nil {
		t.Fatal(err)
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	mi := &metainfo.MetaInfo{InfoBytes: infoBytes}
	torrentPath := filepath.Join(t.TempDir(), "show.torrent")
	f, err := os.Create(torrentPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := mi.Write(f); err != nil {
		t.Fatal(err)
	}
	f.Close()

	cfg := loopbackConfig(t)
	cfg.DataDir = src
	cfg.Seed = true
	seeder, err := torrent.NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { seeder.Close() })
	st, err := seeder.AddTorrent(mi)
	if err != nil {
		t.Fatal(err)
	}
	<-st.GotInfo()
	if err := st.VerifyDataContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-st.Complete().On():
	case <-time.After(10 * time.Second):
		t.Fatalf("seeder only has %d/%d bytes", st.BytesCompleted(), st.Length())
	}
	return torrentPath, seeder, content
}

// The end-to-end path godl's daemon takes for a torrent job with --files
// and a player streaming it: only the selected file is fetched, progress
// counts only that file, and a reader started mid-download gets the
// exact bytes.
func TestSelectAndStreamFromALocalSeeder(t *testing.T) {
	torrentPath, seeder, content := seedLocally(t)

	m, err := newManager(loopbackConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	out := t.TempDir()
	tt, err := m.Add("job", torrentPath, out)
	if err != nil {
		t.Fatal(err)
	}
	<-tt.GotInfo()
	sel, _ := ParseSelection("*.mkv")
	if n, err := m.Select("job", sel); err != nil || n != 1 {
		t.Fatalf("Select(*.mkv) = %d, %v; want 1 file", n, err)
	}
	tt.AddClientPeer(seeder)

	files, selected := m.Files("job")
	idx := -1
	for i, f := range files {
		if filepath.Base(f.Path) == "episode.mkv" {
			idx = i
		}
		if selected[i] != (filepath.Base(f.Path) == "episode.mkv") {
			t.Errorf("file %s selected=%v", f.Path, selected[i])
		}
	}
	if idx < 0 {
		t.Fatalf("episode.mkv missing from %+v", files)
	}

	f, ok := m.File("job", idx)
	if !ok {
		t.Fatal("File() couldn't find the selected file")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	r := f.NewReader()
	r.SetResponsive()
	r.SetContext(ctx)
	got, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content["episode.mkv"]) {
		t.Fatal("streamed bytes don't match the seeded file")
	}

	deadline := time.Now().Add(30 * time.Second)
	for !m.Done("job") {
		if time.Now().After(deadline) {
			done, total, _ := m.Progress("job")
			t.Fatalf("selection never completed: %d/%d", done, total)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, total, _ := m.Progress("job"); total != int64(len(content["episode.mkv"])) {
		t.Errorf("progress total = %d, want only the selected file's %d bytes", total, len(content["episode.mkv"]))
	}
}

// Seed ratios are computed from Uploaded, so it must count what godl's
// own client sends to a peer.
func TestUploadedCountsBytesSentToPeers(t *testing.T) {
	torrentPath, _, content := seedLocally(t)

	m, err := newManager(loopbackConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	src := filepath.Join(t.TempDir(), "src")
	if err := os.MkdirAll(filepath.Join(src, "Show"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range content {
		if err := os.WriteFile(filepath.Join(src, "Show", name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := m.Add("seed", torrentPath, src)
	if err != nil {
		t.Fatal(err)
	}
	<-st.GotInfo()
	if _, err := m.Select("seed", nil); err != nil {
		t.Fatal(err)
	}
	if err := st.VerifyDataContext(t.Context()); err != nil {
		t.Fatal(err)
	}

	leecher, err := torrent.NewClient(loopbackConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	defer leecher.Close()
	mi, err := metainfo.LoadFromFile(torrentPath)
	if err != nil {
		t.Fatal(err)
	}
	lt, err := leecher.AddTorrent(mi)
	if err != nil {
		t.Fatal(err)
	}
	<-lt.GotInfo()
	lt.DownloadAll()
	lt.AddClientPeer(m.client)
	select {
	case <-lt.Complete().On():
	case <-time.After(30 * time.Second):
		t.Fatalf("leecher only got %d/%d bytes", lt.BytesCompleted(), lt.Length())
	}
	if up := m.Uploaded("seed"); up < lt.Length() {
		t.Errorf("Uploaded = %d after a peer downloaded all %d bytes from us", up, lt.Length())
	}
}
