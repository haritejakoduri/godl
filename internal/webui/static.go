package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

//go:embed static
var staticFS embed.FS

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
