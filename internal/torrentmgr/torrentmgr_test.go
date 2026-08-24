package torrentmgr

import "testing"

// TestSpecFromSourceRejectsZeroInfoHash is a regression test: a magnet
// link with an all-zero btih (malformed, but not rejected by
// torrent.TorrentSpecFromMagnetUri's own parsing) used to reach
// client.AddTorrentSpec, which panics rather than erroring on a zero
// info hash — and since Manager.Add is called synchronously from the
// daemon's job-start path, that panic took down the entire daemon
// process, not just this one job. specFromSource must reject it with a
// plain error instead.
func TestSpecFromSourceRejectsZeroInfoHash(t *testing.T) {
	const zeroHashMagnet = "magnet:?xt=urn:btih:0000000000000000000000000000000000000000&dn=faketest"
	_, err := specFromSource(zeroHashMagnet)
	if err == nil {
		t.Fatal("specFromSource(zero-infohash magnet) = nil error, want a rejection")
	}
}

func TestSpecFromSourceAcceptsValidMagnet(t *testing.T) {
	const validMagnet = "magnet:?xt=urn:btih:a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2&dn=faketest"
	spec, err := specFromSource(validMagnet)
	if err != nil {
		t.Fatalf("specFromSource(valid magnet) = %v, want no error", err)
	}
	if spec.InfoHash.IsZero() {
		t.Fatal("specFromSource(valid magnet) produced a zero info hash")
	}
}
