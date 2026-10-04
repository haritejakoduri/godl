package webui

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// The interface works offline and can't be changed by a third party,
// so nothing in it may load from another host — and the page's own
// Content-Security-Policy would block it anyway, silently.
func TestStaticIsSelfContained(t *testing.T) {
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`<script[^>]+src="(https?:)?//`),
		regexp.MustCompile(`<link[^>]+href="(https?:)?//`),
		regexp.MustCompile(`@import`),
		regexp.MustCompile(`url\(\s*["']?(https?:)?//`),
		regexp.MustCompile(`fetch\(\s*["'](https?:)?//`),
		// Inline script/style would be blocked by the CSP.
		regexp.MustCompile(`<script>`),
		regexp.MustCompile(`<style`),
		regexp.MustCompile(` style="`),
		regexp.MustCompile(` on[a-z]+="`),
	}
	err := fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := staticFS.ReadFile(p)
		if err != nil {
			return err
		}
		for _, re := range patterns {
			if m := re.Find(b); m != nil {
				t.Errorf("%s: %q — the page must be self-contained and CSP-clean", p, m)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// Every id the script looks up exists in the page, so a renamed
// element fails here instead of as a null error in someone's browser.
func TestScriptIDsExistInPage(t *testing.T) {
	js, _ := staticFS.ReadFile("static/app.js")
	html, _ := staticFS.ReadFile("static/index.html")
	ids := map[string]bool{}
	for _, m := range regexp.MustCompile(`id="([^"]+)"`).FindAllStringSubmatch(string(html), -1) {
		ids[m[1]] = true
	}
	for _, m := range regexp.MustCompile(`\$\('([^']+)'\)`).FindAllStringSubmatch(string(js), -1) {
		id := m[1]
		if strings.ContainsAny(id, " +") {
			continue
		}
		if !ids[id] {
			t.Errorf("app.js uses #%s, which index.html doesn't have", id)
		}
	}
}
