package reqhdr

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestParseHeader(t *testing.T) {
	name, value, err := ParseHeader("authorization:  Bearer abc ")
	if err != nil || name != "Authorization" || value != "Bearer abc" {
		t.Errorf("ParseHeader = %q, %q, %v", name, value, err)
	}
	for _, bad := range []string{"no-colon", ": empty-name", "Bad Name: x", "X: a\r\nInjected: y"} {
		if _, _, err := ParseHeader(bad); err == nil {
			t.Errorf("ParseHeader(%q) accepted an invalid header", bad)
		}
	}
}

func writeCookies(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cookies.txt")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A browser export holds every site's cookies; each request must only
// carry the ones for its own host, path and scheme, and none that expired.
func TestCookiesAreMatchedPerRequest(t *testing.T) {
	file := writeCookies(t, "# Netscape HTTP Cookie File\n"+
		".example.com\tTRUE\t/\tFALSE\t0\tsite\t1\n"+
		"#HttpOnly_dl.example.com\tFALSE\t/files\tTRUE\t0\tscoped\t2\n"+
		"other.org\tFALSE\t/\tFALSE\t0\tforeign\t3\n"+
		".example.com\tTRUE\t/\tFALSE\t1\texpired\t4\n")
	s, err := Build(nil, file)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"https://dl.example.com/files/a.iso": "site=1; scoped=2",
		"http://dl.example.com/files/a.iso":  "site=1", // scoped is Secure
		"https://dl.example.com/filesystem":  "site=1", // not under /files
		"https://example.com/":               "site=1",
		"https://evil-example.com/":          "",
		"https://other.org/x":                "foreign=3",
	}
	for u, want := range cases {
		req, _ := http.NewRequest(http.MethodGet, u, nil)
		s.Apply(req)
		if got := req.Header.Get("Cookie"); got != want {
			t.Errorf("%s: Cookie = %q, want %q", u, got, want)
		}
	}
}

func TestApplySetsHeadersButNotRange(t *testing.T) {
	s, err := Build([]string{"User-Agent: godl-test", "Range: bytes=0-1", "Host: mirror.example"}, "")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/f", nil)
	req.Header.Set("User-Agent", "default")
	req.Header.Set("Range", "bytes=100-")
	s.Apply(req)
	if got := req.Header.Get("User-Agent"); got != "godl-test" {
		t.Errorf("User-Agent = %q, want the job's own", got)
	}
	if got := req.Header.Get("Range"); got != "bytes=100-" {
		t.Errorf("Range = %q, want the downloader's to survive", got)
	}
	if req.Host != "mirror.example" {
		t.Errorf("Host = %q, want mirror.example", req.Host)
	}
}

func TestNilSetAppliesNothing(t *testing.T) {
	s, err := Build(nil, "")
	if err != nil || s != nil {
		t.Fatalf("Build(nothing) = %v, %v; want nil, nil", s, err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://example.com/f", nil)
	s.Apply(req) // must not panic
	if len(req.Header) != 0 {
		t.Errorf("headers = %v, want none", req.Header)
	}
}

func TestMalformedCookiesFileIsRejected(t *testing.T) {
	if _, err := Build(nil, writeCookies(t, "example.com\tTRUE\t/\n")); err == nil {
		t.Error("a line with too few fields was accepted")
	}
}
