package webui

import (
	"bytes"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// throttlingServer behaves like YouTube's media servers: any one
// request gets its first burst bytes quickly, then the rest at rate
// bytes per second. Range requests are honored.
func throttlingServer(t *testing.T, data []byte, burst, rate int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start, end, ranged := parseRange(r.Header.Get("Range"))
		if end < 0 || end >= int64(len(data)) {
			end = int64(len(data)) - 1
		}
		body := data[start : end+1]
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", itoa(len(body)))
		if ranged {
			w.Header().Set("Content-Range", "bytes "+itoa(int(start))+"-"+itoa(int(end))+"/"+itoa(len(data)))
			w.WriteHeader(http.StatusPartialContent)
		}
		sent := 0
		for sent < len(body) {
			n := min(16<<10, len(body)-sent)
			if _, err := w.Write(body[sent : sent+n]); err != nil {
				return
			}
			sent += n
			if sent > burst {
				time.Sleep(time.Duration(float64(n) / float64(rate) * float64(time.Second)))
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func itoa(n int) string { return string(appendInt(nil, n)) }

func appendInt(b []byte, n int) []byte {
	if n >= 10 {
		b = appendInt(b, n/10)
	}
	return append(b, byte('0'+n%10))
}

func timedGet(t *testing.T, url string, header string) ([]byte, time.Duration) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if header != "" {
		req.Header.Set("Range", header)
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return b, time.Since(start)
}

// Through the relay, a throttled stream arrives at the burst speed,
// intact, where a single long request crawls.
func TestRelayBeatsPerRequestThrottling(t *testing.T) {
	data := make([]byte, 3<<19)
	rand.Read(data)
	src := throttlingServer(t, data, 256<<10, 600<<10) // 256 KB fast, then 600 KB/s

	var r relay
	defer r.close()
	viaRelay, err := r.url(src.URL, "video", http.Header{"X-Test": {"1"}}, "godl-test", 256<<10)
	if err != nil {
		t.Fatal(err)
	}

	got, relayed := timedGet(t, viaRelay, "")
	if !bytes.Equal(got, data) {
		t.Fatalf("relayed %d bytes, not the original %d", len(got), len(data))
	}
	_, direct := timedGet(t, src.URL, "")
	t.Logf("1.5 MB: one long request %v, through the relay %v", direct.Round(time.Millisecond), relayed.Round(time.Millisecond))
	if relayed*3 > direct {
		t.Errorf("the relay (%v) should be several times faster than one throttled request (%v)", relayed, direct)
	}
}

// ffmpeg seeks by asking for a byte range; the relay has to answer
// exactly that range, with the headers a range answer carries.
func TestRelayRanges(t *testing.T) {
	data := make([]byte, 1<<20)
	rand.Read(data)
	src := throttlingServer(t, data, 1<<30, 1<<30)
	var r relay
	defer r.close()
	u, _ := r.url(src.URL, "video", nil, "", 100<<10)

	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Range", "bytes=500000-")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || resp.Header.Get("Content-Range") != "bytes 500000-1048575/1048576" {
		t.Errorf("open-ended range: %s %q", resp.Status, resp.Header.Get("Content-Range"))
	}
	if !bytes.Equal(body, data[500000:]) {
		t.Errorf("open-ended range: got %d bytes, want the %d from the offset", len(body), len(data)-500000)
	}

	got, _ := timedGet(t, u, "bytes=10-300009")
	if !bytes.Equal(got, data[10:300010]) {
		t.Errorf("closed range spanning pieces: got %d bytes, want 300000", len(got))
	}

	bad, _ := http.Get("http://" + r.addr + "/not-a-token/video")
	if bad.StatusCode != http.StatusNotFound {
		t.Errorf("unknown token: got %d, want 404", bad.StatusCode)
	}
	bad.Body.Close()
}

func TestParseContentRange(t *testing.T) {
	if s, e, tot, ok := parseContentRange("bytes 0-99/1000"); !ok || s != 0 || e != 99 || tot != 1000 {
		t.Errorf("parseContentRange = %d %d %d %v", s, e, tot, ok)
	}
	if _, _, _, ok := parseContentRange("bytes */1000"); ok {
		t.Error("an unsatisfied-range header should not parse")
	}
}
