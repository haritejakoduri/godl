package webui

import (
	"embed"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

//go:embed static
var staticFS embed.FS

func init() {
	// http.FileServer types a response from the file extension, and
	// Go's table has no entry for .webmanifest — so without this the
	// manifest goes out sniffed, as text/plain. Combined with the
	// nosniff header Guard sets, a browser is entitled to refuse it
	// outright, and a refused manifest means no install prompt and
	// nothing anywhere saying why.
	if err := mime.AddExtensionType(".webmanifest", "application/manifest+json"); err != nil {
		panic(err) // a constant type string, so this cannot fail
	}
}

// Static serves the page itself: one HTML file, its stylesheet and its
// script, compiled into the binary. Nothing is fetched from anywhere
// else, so the interface works offline and can't be changed by a CDN.
func Static() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err) // the directory is embedded at build time
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Revalidate every load: the files change with each godl
		// release, and a stale script against a new API is a bug
		// report waiting to happen.
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

// PublicPaths are the paths Guard serves without authenticating: the
// web manifest, the icons it names, and /favicon.ico.
//
// A manifest is fetched without credentials unless the link element
// asks otherwise, so behind the normal check the browser gets a 401,
// never reads the manifest, and silently never offers to install godl
// as an app — with no error anywhere for anyone to notice. The icons
// are the same art compiled into every published binary and the
// manifest carries no token, so there is nothing here to protect.
//
// Listed one by one rather than by prefix so this says exactly what it
// opens; static_test.go checks nothing in the embedded directory was
// added without being considered here.
func PublicPaths() []string {
	return []string{
		"/manifest.webmanifest",
		"/favicon.ico",
		"/icon-180.png",
		"/icon-192.png",
		"/icon-512.png",
		"/icon-maskable-512.png",
	}
}

// tokenFile holds the local-mode token: what "godl web" puts in the
// link it opens. It outlives daemon restarts so a bookmarked tab's
// cookie keeps working.
const tokenFile = "webui.token"

// Token returns the token stored under dataDir, creating one the first
// time. The file is readable only by its owner — it's the whole
// credential for the interface in local mode.
func Token(dataDir string) (string, error) {
	p := filepath.Join(dataDir, tokenFile)
	if b, err := os.ReadFile(p); err == nil {
		if tok := strings.TrimSpace(string(b)); len(tok) >= 32 {
			return tok, nil
		}
	}
	tok := randomID()
	if err := os.WriteFile(p, []byte(tok+"\n"), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}
