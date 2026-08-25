// Package fileserver turns a local directory into a small HTTP(S)
// server: a full WebDAV endpoint (so any WebDAV client — Windows
// Explorer, macOS Finder, Linux's GVFS, rclone, or godl's own
// internal/webdav.Client — can mount or browse it, including bulk
// multi-file download through godl's existing TUI browser) plus a
// plain browser-friendly page for people who'd rather just click
// links, with a "download selected as one .zip" action since a bare
// browser has no native way to select several files and download them
// as a single action over plain HTTP.
package fileserver

import (
	"archive/zip"
	"crypto/subtle"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/net/webdav"
)

// Config configures a Server built by New.
type Config struct {
	// Root is the local directory being served. Must exist and be a
	// directory.
	Root string

	// Username/Password, if both non-empty, require HTTP Basic Auth on
	// every request (WebDAV, the browse page, and zip downloads alike).
	// Leave both empty for no auth. New itself doesn't second-guess
	// that choice — refusing to run unauthenticated on a non-loopback
	// address is a policy call left to the caller (see cmd/serve.go).
	Username string
	Password string

	// ReadOnly, when true, rejects any WebDAV method that could modify
	// the served tree (everything except GET/HEAD/OPTIONS/PROPFIND) —
	// this is meant as a download server, not a general remote drive,
	// unless the caller explicitly opts into write access.
	ReadOnly bool
}

// New builds the server's http.Handler: a WebDAV endpoint at /dav/, a
// browsable HTML page at /, and a same-origin zip-download endpoint at
// /zip that the browse page's "download selected" button posts to.
func New(cfg Config) (http.Handler, error) {
	absRoot, err := filepath.Abs(cfg.Root)
	if err != nil {
		return nil, fmt.Errorf("resolving root directory: %w", err)
	}
	fi, err := os.Stat(absRoot)
	if err != nil {
		return nil, fmt.Errorf("root directory: %w", err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("root %q is not a directory", absRoot)
	}

	dav := &webdav.Handler{
		FileSystem: webdav.Dir(absRoot),
		LockSystem: webdav.NewMemLS(),
	}

	mux := http.NewServeMux()
	mux.Handle("/dav/", methodFilter(cfg.ReadOnly, http.StripPrefix("/dav", dav)))
	mux.HandleFunc("/", browseHandler(absRoot))
	mux.HandleFunc("/zip", zipHandler(absRoot))

	var h http.Handler = mux
	if cfg.Username != "" || cfg.Password != "" {
		h = basicAuth(cfg.Username, cfg.Password, h)
	}
	return h, nil
}

func basicAuth(username, password string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		// subtle.ConstantTimeCompare on mismatched lengths just returns
		// 0 (not a panic), so this is safe even when u/p differ in
		// length from the configured credentials.
		if !ok ||
			subtle.ConstantTimeCompare([]byte(u), []byte(username)) != 1 ||
			subtle.ConstantTimeCompare([]byte(p), []byte(password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="godl serve"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// readOnlyAllowedMethods is an allow-list, not a deny-list, so any
// WebDAV method this package didn't specifically anticipate is denied
// by default rather than accidentally let through.
var readOnlyAllowedMethods = map[string]bool{
	http.MethodGet:     true,
	http.MethodHead:    true,
	http.MethodOptions: true,
	"PROPFIND":         true,
}

func methodFilter(readOnly bool, next http.Handler) http.Handler {
	if !readOnly {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !readOnlyAllowedMethods[r.Method] {
			http.Error(w, "server is read-only (start godl serve with --allow-write to enable uploads/deletes)", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// resolveUnderRoot joins root and a client-supplied relative path
// (always "/"-separated, as used in URLs — see browseHandler/
// zipHandler), refusing to resolve outside root even if rel contains
// ".." segments. rel comes straight from a query parameter or form
// value a client controls directly, so this is load-bearing, not
// defense in depth.
func resolveUnderRoot(root, rel string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if rel == "" {
		return absRoot, nil
	}
	joined := filepath.Join(absRoot, filepath.FromSlash(rel))
	absJoined, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	if absJoined != absRoot && !strings.HasPrefix(absJoined, absRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes served directory")
	}
	return absJoined, nil
}

type browseEntry struct {
	Name     string
	RelPath  string // "/"-separated, relative to root — the ?dir= / f= value
	DavPath  string // "/"-separated, absolute from root, for the /dav/ GET link
	IsDir    bool
	IsParent bool // the synthetic ".." row; not selectable
	Size     string
}

type browseCrumb struct {
	Name string
	Path string
}

type browseData struct {
	Title       string
	Dir         string
	Breadcrumbs []browseCrumb
	Entries     []browseEntry
}

var browseTmpl = template.Must(template.New("browse").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>{{.Title}}</title>
<style>
  body { font-family: system-ui, -apple-system, sans-serif; margin: 2rem; color: #1a1a1a; }
  h2 { font-size: 1.1rem; font-weight: 600; }
  table { border-collapse: collapse; width: 100%; max-width: 760px; }
  td, th { text-align: left; padding: 0.35rem 0.6rem; border-bottom: 1px solid #eee; }
  th { font-size: 0.8rem; color: #666; text-transform: uppercase; }
  a { color: #0969da; text-decoration: none; }
  a:hover { text-decoration: underline; }
  .size { color: #666; font-variant-numeric: tabular-nums; white-space: nowrap; }
  .crumbs { color: #666; margin-bottom: 1rem; }
  .actions { margin-top: 1rem; }
  button { padding: 0.5rem 1rem; font-size: 0.9rem; cursor: pointer; }
</style></head>
<body>
<h2>{{.Title}}</h2>
<p class="crumbs"><a href="/">root</a>{{range .Breadcrumbs}} / <a href="/?dir={{.Path}}">{{.Name}}</a>{{end}}</p>
<form method="post" action="/zip">
<table>
<tr><th></th><th>Name</th><th>Size</th></tr>
{{range .Entries}}
<tr>
<td>{{if not .IsParent}}<input type="checkbox" name="f" value="{{.RelPath}}">{{end}}</td>
<td>{{if .IsDir}}<a href="/?dir={{.RelPath}}">{{.Name}}/</a>{{else}}<a href="/dav{{.DavPath}}">{{.Name}}</a>{{end}}</td>
<td class="size">{{.Size}}</td>
</tr>
{{end}}
</table>
<div class="actions">
<button type="button" onclick="document.querySelectorAll('input[type=checkbox]').forEach(c=>c.checked=true)">Select all</button>
<button type="submit">Download selected (.zip)</button>
</div>
</form>
</body></html>`))

func browseHandler(root string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		dir := strings.Trim(r.URL.Query().Get("dir"), "/")
		abs, err := resolveUnderRoot(root, dir)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		osEntries, err := os.ReadDir(abs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		sort.Slice(osEntries, func(i, j int) bool {
			if osEntries[i].IsDir() != osEntries[j].IsDir() {
				return osEntries[i].IsDir()
			}
			return osEntries[i].Name() < osEntries[j].Name()
		})

		data := browseData{Title: "godl — /" + dir, Dir: dir}
		if dir != "" {
			parts := strings.Split(dir, "/")
			for i, p := range parts {
				data.Breadcrumbs = append(data.Breadcrumbs, browseCrumb{Name: p, Path: strings.Join(parts[:i+1], "/")})
			}
			parent := strings.Join(parts[:len(parts)-1], "/")
			data.Entries = append(data.Entries, browseEntry{Name: "..", RelPath: parent, IsDir: true, IsParent: true})
		}
		for _, e := range osEntries {
			relPath := e.Name()
			if dir != "" {
				relPath = dir + "/" + relPath
			}
			be := browseEntry{Name: e.Name(), RelPath: relPath, DavPath: "/" + relPath, IsDir: e.IsDir()}
			if !e.IsDir() {
				if info, err := e.Info(); err == nil {
					be.Size = humanSize(info.Size())
				}
			} else {
				be.Size = "-"
			}
			data.Entries = append(data.Entries, be)
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		browseTmpl.Execute(w, data)
	}
}

// zipHandler streams a .zip of every selected file/folder (form field
// "f", repeated once per selection — see browseTmpl's checkboxes)
// straight to the response, without staging anything on disk. A
// selected folder is walked recursively, preserving its own name and
// structure inside the archive — the same "keep the folder's own
// name" principle internal/daemon/webdav.go's webdavLocalPath applies
// to WebDAV folder downloads.
func zipHandler(root string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		selected := r.Form["f"]
		if len(selected) == 0 {
			http.Error(w, "no files selected", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="godl-download.zip"`)

		zw := zip.NewWriter(w)
		defer zw.Close()

		added := map[string]bool{}
		for _, rel := range selected {
			abs, err := resolveUnderRoot(root, strings.Trim(rel, "/"))
			if err != nil {
				continue // silently skip anything that doesn't resolve safely
			}
			if addToZip(zw, root, abs, added) != nil {
				return // client will see a truncated (and thus visibly broken) zip
			}
		}
	}
}

func addToZip(zw *zip.Writer, root, abs string, added map[string]bool) error {
	return filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		zipName := filepath.ToSlash(rel)
		if added[zipName] {
			return nil // two overlapping selections (a folder and one of its own files)
		}
		added[zipName] = true

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		zf, err := zw.Create(zipName)
		if err != nil {
			return err
		}
		_, err = io.Copy(zf, f)
		return err
	})
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
