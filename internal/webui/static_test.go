package webui

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path"
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
		// Only the text sources: these patterns describe how a page
		// pulls in something external, which a PNG cannot do, and
		// regex-scanning compressed image data for "@import" invites
		// a baffling failure from a byte sequence that means nothing.
		switch path.Ext(p) {
		case ".html", ".css", ".js", ".webmanifest":
		default:
			return nil
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

// TestPublicPathsCoverTheInstallAssets keeps PublicPaths honest. It is
// a hand-written list, and an icon added to the directory but not to
// it is served behind the normal check — which for a manifest icon
// means the install prompt quietly stops appearing, with nothing
// failing anywhere.
func TestPublicPathsCoverTheInstallAssets(t *testing.T) {
	open := map[string]bool{}
	for _, p := range PublicPaths() {
		open[p] = true
	}
	entries, err := fs.ReadDir(staticFS, "static")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		n := e.Name()
		isInstallAsset := n == "manifest.webmanifest" || n == "favicon.ico" ||
			strings.HasPrefix(n, "icon-")
		if !isInstallAsset {
			continue
		}
		if !open["/"+n] {
			t.Errorf("static/%s is an install asset but PublicPaths() does not serve it unauthenticated", n)
		}
	}
	// And nothing is opened that isn't actually there, which would
	// mean a stale entry hiding a rename.
	for _, p := range PublicPaths() {
		if _, err := fs.Stat(staticFS, "static"+p); err != nil {
			t.Errorf("PublicPaths() opens %s, which is not in the embedded directory", p)
		}
	}
}

// TestManifestNamesIconsThatExist: a manifest pointing at a missing
// icon is not an error the browser reports — it just declines to offer
// the install, so this is the only place it would be caught.
func TestManifestNamesIconsThatExist(t *testing.T) {
	b, err := staticFS.ReadFile("static/manifest.webmanifest")
	if err != nil {
		t.Fatal(err)
	}
	var m struct {
		Name     string `json:"name"`
		Display  string `json:"display"`
		StartURL string `json:"start_url"`
		Icons    []struct {
			Src     string `json:"src"`
			Sizes   string `json:"sizes"`
			Purpose string `json:"purpose"`
		} `json:"icons"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	if m.Display != "standalone" {
		t.Errorf("display = %q, want standalone — that is what gives the installed app its own window", m.Display)
	}
	if m.StartURL == "" {
		t.Error("start_url is empty")
	}
	var maskable, any int
	for _, ic := range m.Icons {
		if _, err := fs.Stat(staticFS, "static/"+ic.Src); err != nil {
			t.Errorf("manifest names icon %q, which is not embedded", ic.Src)
		}
		switch ic.Purpose {
		case "maskable":
			maskable++
		default:
			any++
		}
	}
	// Both slots matter: without a maskable one the platform crops the
	// transparent icon onto a background of its choosing.
	if any == 0 || maskable == 0 {
		t.Errorf("manifest has %d ordinary and %d maskable icons, want at least one of each", any, maskable)
	}
}

// TestPageDeclaresTheInstallBits: the manifest and icon only take
// effect if the page links them, and a page that forgets looks
// completely normal.
func TestPageDeclaresTheInstallBits(t *testing.T) {
	html, err := staticFS.ReadFile("static/index.html")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`rel="manifest"`,
		`rel="icon"`,
		`rel="apple-touch-icon"`,
		`name="theme-color"`,
	} {
		if !strings.Contains(string(html), want) {
			t.Errorf("index.html does not declare %s", want)
		}
	}
}

// TestManifestIsServedAsAManifest guards the registration in this
// package's init. Without it http.FileServer finds no .webmanifest in
// Go's MIME table, sniffs the file and sends text/plain; with the
// nosniff header Guard also sets, a browser may then refuse the
// manifest — no install prompt, no error, nothing to debug.
func TestManifestIsServedAsAManifest(t *testing.T) {
	w := httptest.NewRecorder()
	Static().ServeHTTP(w, httptest.NewRequest("GET", "/manifest.webmanifest", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /manifest.webmanifest = %d, want 200", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/manifest+json") {
		t.Errorf("Content-Type = %q, want application/manifest+json", ct)
	}
}
