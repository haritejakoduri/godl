package cmd

import (
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"godl/internal/guide"
)

var (
	guideBlockRe   = regexp.MustCompile(`(?s)<div class="term"><pre>(.*?)</pre>`)
	guideCommentRe = regexp.MustCompile(`(?s)<span class="c">.*?</span>`)
	guideTagRe     = regexp.MustCompile(`<[^>]+>`)
	guideInlineRe  = regexp.MustCompile(`<code>(godl [^<]*)</code>`)
)

// guideCommandLines returns every "godl ..." command the guide shows,
// from its copyable terminal blocks and from inline <code> mentions,
// as plain text with the reader-facing "# comments" removed.
func guideCommandLines(t *testing.T) []string {
	t.Helper()
	page := string(guide.HTML)
	var lines []string
	for _, m := range guideBlockRe.FindAllStringSubmatch(page, -1) {
		block := guideTagRe.ReplaceAllString(guideCommentRe.ReplaceAllString(m[1], ""), "")
		for _, line := range strings.Split(html.UnescapeString(block), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				lines = append(lines, line)
			}
		}
	}
	for _, m := range guideInlineRe.FindAllStringSubmatch(page, -1) {
		lines = append(lines, html.UnescapeString(m[1]))
	}
	if len(lines) < 30 {
		t.Fatalf("found only %d command lines in the guide — did its markup change?", len(lines))
	}
	return lines
}

// shellFields splits a command line on spaces, keeping "double quoted"
// arguments together — all the quoting the guide's examples use.
func shellFields(line string) []string {
	var fields []string
	var cur strings.Builder
	inQuote, has := false, false
	for _, r := range line {
		switch {
		case r == '"':
			inQuote, has = !inQuote, true
		case r == ' ' && !inQuote:
			if has {
				fields = append(fields, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	if has {
		fields = append(fields, cur.String())
	}
	return fields
}

// TestGuideCommandsExist keeps the guide honest: every command it
// tells a reader to type must name a real subcommand, and every flag
// on that line must be one the subcommand actually has. A flag renamed
// in the CLI without the guide following fails here.
func TestGuideCommandsExist(t *testing.T) {
	for _, line := range guideCommandLines(t) {
		fields := shellFields(line)
		if len(fields) == 0 || fields[0] != "godl" {
			t.Errorf("guide command %q doesn't start with godl", line)
			continue
		}
		args := fields[1:]
		c, rest, err := rootCmd.Find(args)
		if err != nil {
			t.Errorf("guide command %q: %v", line, err)
			continue
		}
		// A first word that isn't a flag or placeholder, on a line
		// that resolved no further than the root, is a subcommand that
		// doesn't exist.
		if c == rootCmd && len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			t.Errorf("guide command %q: no such subcommand %q", line, args[0])
			continue
		}
		for _, arg := range rest {
			if !strings.HasPrefix(arg, "-") || arg == "-" {
				continue
			}
			name := strings.SplitN(strings.TrimLeft(arg, "-"), "=", 2)[0]
			found := c.Flags().Lookup(name) != nil || c.InheritedFlags().Lookup(name) != nil || name == "help"
			if !strings.HasPrefix(arg, "--") {
				found = len(name) == 1 && c.Flags().ShorthandLookup(name) != nil
			}
			if !found {
				t.Errorf("guide command %q: %q has no flag %s", line, c.CommandPath(), arg)
			}
		}
	}
}

// TestGuideIsSelfContained: the guide has to work offline, so nothing
// in it may load from the network — links the reader clicks are fine,
// resources the page fetches by itself are not.
func TestGuideIsSelfContained(t *testing.T) {
	page := string(guide.HTML)
	for _, pattern := range []string{`<script[^>]+src=`, `<link[^>]+href=`, `<img[^>]+src="http`, `@import`, `url\(\s*["']?http`} {
		if loc := regexp.MustCompile(pattern).FindString(page); loc != "" {
			t.Errorf("the guide loads an external resource (%s); it must stay self-contained", loc)
		}
	}
}

func TestGuideHandler(t *testing.T) {
	srv := httptest.NewServer(guideHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Errorf("GET / = %s (%s), want 200 text/html", resp.Status, resp.Header.Get("Content-Type"))
	}

	resp, err = http.Get(srv.URL + "/anything-else")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /anything-else = %s, want 404", resp.Status)
	}
}
