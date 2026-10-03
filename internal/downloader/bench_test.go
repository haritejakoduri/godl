package downloader

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// BenchmarkDownload measures the chunked HTTP download path end to end
// against a loopback server — godl's own overhead, with the network
// taken out. MB/s is the number to watch: a change elsewhere (the web
// interface, a new setting) shouldn't move it.
func BenchmarkDownload(b *testing.B) {
	const size = 64 << 20
	body := bytes.Repeat([]byte("godl-benchmark-"), size/15+1)[:size]
	modTime := time.Unix(0, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "f.bin", modTime, bytes.NewReader(body))
	}))
	defer srv.Close()

	for _, conc := range []int{1, 4} {
		b.Run("connections="+strconv.Itoa(conc), func(b *testing.B) {
			b.SetBytes(size)
			dir := b.TempDir()
			for i := 0; i < b.N; i++ {
				out := filepath.Join(dir, "out-"+strconv.Itoa(i)+".bin")
				res, err := Run(context.Background(), Options{URL: srv.URL, OutputPath: out, Concurrency: conc})
				if err != nil || !res.Completed {
					b.Fatalf("Run: %+v, %v", res, err)
				}
			}
		})
	}
}
