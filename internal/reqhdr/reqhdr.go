// Package reqhdr applies a job's extra request headers and cookies
// (from --header/--user-agent/--referer/--cookie and a Netscape
// cookies.txt file) to outgoing HTTP requests.
//
// Cookies are matched per request URL rather than put in a fixed Cookie
// header, so a cookie file exported from a browser — which holds cookies
// for every site — only ever sends a site its own cookies. Go's
// http.Client already drops Cookie/Authorization headers when a redirect
// leaves the original domain, so a redirect can't leak them either.
package reqhdr

import (
	"bufio"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Set is a parsed, ready-to-apply set of headers and cookies. A nil *Set
// applies nothing, so callers never need to check.
type Set struct {
	header  http.Header
	cookies []cookie
}

type cookie struct {
	domain     string // without a leading dot
	subdomains bool
	path       string
	secure     bool
	expires    int64 // unix seconds, 0 = session cookie
	name       string
	value      string
}

// Build parses "Name: value" header lines and, when cookiesFile is set,
// a Netscape-format cookies file. It returns nil (not an empty Set) when
// there is nothing to apply.
func Build(headers []string, cookiesFile string) (*Set, error) {
	if len(headers) == 0 && cookiesFile == "" {
		return nil, nil
	}
	s := &Set{header: http.Header{}}
	for _, h := range headers {
		name, value, err := ParseHeader(h)
		if err != nil {
			return nil, err
		}
		s.header.Add(name, value)
	}
	if cookiesFile != "" {
		cs, err := loadCookies(cookiesFile)
		if err != nil {
			return nil, err
		}
		s.cookies = cs
	}
	return s, nil
}

// ParseHeader splits a curl-style "Name: value" header.
func ParseHeader(h string) (name, value string, err error) {
	name, value, ok := strings.Cut(h, ":")
	name = strings.TrimSpace(name)
	if !ok || name == "" || strings.ContainsAny(name, " \t\r\n") {
		return "", "", fmt.Errorf("invalid header %q: expected \"Name: value\"", h)
	}
	if strings.ContainsAny(value, "\r\n") {
		return "", "", fmt.Errorf("invalid header %q: value contains a line break", h)
	}
	return http.CanonicalHeaderKey(name), strings.TrimSpace(value), nil
}

// Apply sets the headers on req, replacing any the caller already set
// (such as a default User-Agent), then adds the cookies that match
// req.URL. The Range header is left alone: the downloader owns it.
func (s *Set) Apply(req *http.Request) {
	if s == nil {
		return
	}
	for name, values := range s.header {
		switch name {
		case "Range":
			continue
		case "Host":
			req.Host = values[len(values)-1]
			continue
		}
		req.Header[name] = append([]string(nil), values...)
	}
	if pairs := s.cookiesFor(req.URL, time.Now()); len(pairs) > 0 {
		all := strings.Join(pairs, "; ")
		if existing := req.Header.Get("Cookie"); existing != "" {
			all = existing + "; " + all
		}
		req.Header.Set("Cookie", all)
	}
}

func (s *Set) cookiesFor(u *url.URL, now time.Time) []string {
	if u == nil {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	var out []string
	for _, c := range s.cookies {
		if c.expires != 0 && c.expires < now.Unix() {
			continue
		}
		if c.secure && u.Scheme != "https" {
			continue
		}
		if host != c.domain && !(c.subdomains && strings.HasSuffix(host, "."+c.domain)) {
			continue
		}
		if !pathMatches(path, c.path) {
			continue
		}
		out = append(out, c.name+"="+c.value)
	}
	return out
}

// pathMatches is RFC 6265's path-match: an exact match, or cookiePath
// being a directory prefix of reqPath.
func pathMatches(reqPath, cookiePath string) bool {
	if cookiePath == "" || cookiePath == "/" || reqPath == cookiePath {
		return true
	}
	if !strings.HasPrefix(reqPath, cookiePath) {
		return false
	}
	return strings.HasSuffix(cookiePath, "/") || reqPath[len(cookiePath)] == '/'
}

// loadCookies reads a Netscape/Mozilla cookies.txt, the format browser
// "export cookies" extensions, curl and yt-dlp all use: seven
// tab-separated fields per line, with "#HttpOnly_" marking an HttpOnly
// cookie rather than a comment.
func loadCookies(path string) ([]cookie, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("reading cookies file: %w", err)
	}
	defer f.Close()
	var out []cookie
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimRight(sc.Text(), "\r")
		line = strings.TrimPrefix(line, "#HttpOnly_")
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 7 {
			return nil, fmt.Errorf("cookies file %s line %d: expected 7 tab-separated fields (Netscape cookies.txt format)", path, lineNo)
		}
		domain := strings.ToLower(strings.TrimSpace(fields[0]))
		expires, _ := strconv.ParseInt(strings.TrimSpace(fields[4]), 10, 64)
		out = append(out, cookie{
			domain:     strings.TrimPrefix(domain, "."),
			subdomains: strings.EqualFold(fields[1], "TRUE") || strings.HasPrefix(domain, "."),
			path:       fields[2],
			secure:     strings.EqualFold(fields[3], "TRUE"),
			expires:    expires,
			name:       fields[5],
			value:      strings.Join(fields[6:], "\t"),
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading cookies file: %w", err)
	}
	return out, nil
}
