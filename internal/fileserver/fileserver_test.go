package fileserver

import (
	"archive/zip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"godl/internal/webdav"
)

// writeTree creates root/docs/{a.txt,b.txt} and root/photos/vacation/img.jpg
// under a fresh temp dir and returns root.
func writeTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(root, "docs"), 0o755))
	must(os.MkdirAll(filepath.Join(root, "photos", "vacation"), 0o755))
	must(os.WriteFile(filepath.Join(root, "docs", "a.txt"), []byte("hello a"), 0o644))
	must(os.WriteFile(filepath.Join(root, "docs", "b.txt"), []byte("hello b"), 0o644))
	must(os.WriteFile(filepath.Join(root, "photos", "vacation", "img.jpg"), []byte("fake jpeg"), 0o644))
	return root
}

func TestNewRejectsNonDirectoryRoot(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{Root: file}); err == nil {
		t.Fatal("New with a file (not directory) root should error")
	}
}

func TestWebDAVInteropListStatDownload(t *testing.T) {
	root := writeTree(t)
	h, err := New(Config{Root: root, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	client, err := webdav.New(srv.URL+"/dav/", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	entries, err := client.List(ctx, "/docs")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("List(/docs) = %v, want 2 entries", entries)
	}

	stat, err := client.Stat(ctx, "/photos")
	if err != nil {
		t.Fatal(err)
	}
	if !stat.IsDir {
		t.Fatal("Stat(/photos) should report a directory")
	}

	files, err := client.Walk(ctx, "/photos")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || filepath.Base(files[0].Path) != "img.jpg" {
		t.Fatalf("Walk(/photos) = %v, want exactly img.jpg", files)
	}

	dst := filepath.Join(t.TempDir(), "img.jpg")
	if _, err := client.Download(ctx, files[0].Path, dst, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(dst)
	if err != nil || string(data) != "fake jpeg" {
		t.Errorf("downloaded content = %q, %v", data, err)
	}
}

func TestReadOnlyRejectsWrites(t *testing.T) {
	root := writeTree(t)
	h, err := New(Config{Root: root, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/dav/docs/new.txt", strings.NewReader("nope"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("PUT under read-only server = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "new.txt")); err == nil {
		t.Error("read-only server should not have written the file")
	}
}

func TestReadWriteAllowsPut(t *testing.T) {
	root := writeTree(t)
	h, err := New(Config{Root: root, ReadOnly: false})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/dav/docs/new.txt", strings.NewReader("hi"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT with --allow-write = %d, want a success status", resp.StatusCode)
	}
	data, err := os.ReadFile(filepath.Join(root, "docs", "new.txt"))
	if err != nil || string(data) != "hi" {
		t.Errorf("written content = %q, %v", data, err)
	}
}

func TestAuthRequiredOnAllEndpoints(t *testing.T) {
	root := writeTree(t)
	h, err := New(Config{Root: root, Username: "alice", Password: "secret", ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	for _, path := range []string{"/", "/dav/"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("GET %s without credentials = %d, want %d", path, resp.StatusCode, http.StatusUnauthorized)
		}
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.SetBasicAuth("alice", "wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("GET / with wrong password = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}

	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	req.SetBasicAuth("alice", "secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET / with correct credentials = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestBrowsePageListsAndNavigates(t *testing.T) {
	root := writeTree(t)
	h, err := New(Config{Root: root, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	page := string(body)
	if !strings.Contains(page, "docs") || !strings.Contains(page, "photos") {
		t.Errorf("root browse page missing top-level entries:\n%s", page)
	}

	resp2, err := http.Get(srv.URL + "/?dir=" + url.QueryEscape("photos/vacation"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	page2 := string(body2)
	if !strings.Contains(page2, "img.jpg") {
		t.Errorf("browse page for photos/vacation missing img.jpg:\n%s", page2)
	}
}

// TestResolveUnderRootRejectsEscapes is a direct unit test of the
// security boundary every request path goes through — every value
// filepath.IsLocal itself documents as unsafe must be rejected, since
// rel here is always a client-controlled query/form value.
func TestResolveUnderRootRejectsEscapes(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{
		"../secret",
		"../../etc/passwd",
		"a/../../secret",
		"/etc/passwd", // absolute, even without ".."
		"..",
	} {
		if _, err := resolveUnderRoot(root, rel); err == nil {
			t.Errorf("resolveUnderRoot(root, %q) should have been rejected", rel)
		}
	}
}

func TestResolveUnderRootAllowsOrdinaryPaths(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"a.txt", "sub/a.txt", "sub/deeper/a.txt"} {
		abs, err := resolveUnderRoot(root, rel)
		if err != nil {
			t.Errorf("resolveUnderRoot(root, %q) = %v, want no error", rel, err)
			continue
		}
		if !strings.HasPrefix(abs, root) {
			t.Errorf("resolveUnderRoot(root, %q) = %q, want it under %q", rel, abs, root)
		}
	}
}

func TestBrowseRejectsPathTraversal(t *testing.T) {
	root := writeTree(t)
	h, err := New(Config{Root: root, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/?dir=" + url.QueryEscape("../../../../etc"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("dir=../../../../etc should not succeed, got 200:\n%s", body)
	}
}

// TestZipDownloadPreservesStructure is the multi-file-at-once feature
// this package exists for: selecting entries spanning two different
// folders and confirming the returned zip contains exactly those
// files, under their real relative paths (including the selected
// folder's own name, not flattened).
func TestZipDownloadPreservesStructure(t *testing.T) {
	root := writeTree(t)
	h, err := New(Config{Root: root, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	form := url.Values{"f": []string{"docs/a.txt", "photos/vacation"}}
	resp, err := http.PostForm(srv.URL+"/zip", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /zip = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", ct)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(strings.NewReader(string(body)), int64(len(body)))
	if err != nil {
		t.Fatalf("response isn't a valid zip: %v", err)
	}

	got := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(data)
	}

	want := map[string]string{
		"docs/a.txt":              "hello a",
		"photos/vacation/img.jpg": "fake jpeg",
	}
	if len(got) != len(want) {
		t.Fatalf("zip contains %v, want exactly %v", got, want)
	}
	for name, content := range want {
		if got[name] != content {
			t.Errorf("zip entry %q = %q, want %q", name, got[name], content)
		}
	}
}

// TestZipDownloadRejectsPathTraversal is a regression test: a crafted
// "f" form value must never let the response include a file outside
// the served root.
func TestZipDownloadRejectsPathTraversal(t *testing.T) {
	root := writeTree(t)
	secret := filepath.Join(filepath.Dir(root), "secret.txt")
	if err := os.WriteFile(secret, []byte("outside root"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(secret)

	h, err := New(Config{Root: root, ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()

	form := url.Values{"f": []string{"../secret.txt"}}
	resp, err := http.PostForm(srv.URL+"/zip", form)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	zr, err := zip.NewReader(strings.NewReader(string(body)), int64(len(body)))
	if err != nil {
		// A malformed/empty zip (nothing valid was added) is an
		// acceptable way to reject this — either way, no data escaped.
		return
	}
	for _, f := range zr.File {
		if strings.Contains(f.Name, "secret") {
			t.Fatalf("zip contains %q — path traversal escaped the served root", f.Name)
		}
	}
}
