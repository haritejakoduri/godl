package jsruntime

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFindPrefersARuntimeOnPATH(t *testing.T) {
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })
	lookPath = func(name string) (string, error) {
		if name == "node" {
			return "/opt/node/bin/node", nil
		}
		return "", errors.New("not found")
	}
	t.Setenv("GODL_DATA_DIR", t.TempDir())
	name, path, err := Find(context.Background(), nil)
	if err != nil || name != "node" || path != "/opt/node/bin/node" {
		t.Fatalf("Find = %q %q %v, want node from PATH (deno isn't there)", name, path, err)
	}
	if got := Args(context.Background(), nil); len(got) != 2 || got[1] != "node:/opt/node/bin/node" {
		t.Errorf("Args = %q", got)
	}
}

func TestFindUsesAnInstalledCopy(t *testing.T) {
	orig := lookPath
	t.Cleanup(func() { lookPath = orig })
	lookPath = func(string) (string, error) { return "", errors.New("not found") }
	data := t.TempDir()
	t.Setenv("GODL_DATA_DIR", data)
	dest := filepath.Join(data, "bin", exeName())
	os.MkdirAll(filepath.Dir(dest), 0o755)
	os.WriteFile(dest, []byte("#!/bin/sh\n"), 0o755)

	name, path, err := Find(context.Background(), nil)
	if err != nil || name != "deno" || path != dest {
		t.Fatalf("Find = %q %q %v, want godl's own deno at %s (no download)", name, path, err, dest)
	}
}

func TestExtract(t *testing.T) {
	dir := t.TempDir()
	zp := filepath.Join(dir, "d.zip")
	f, _ := os.Create(zp)
	zw := zip.NewWriter(f)
	w, _ := zw.Create(exeName())
	w.Write([]byte("binary"))
	zw.Close()
	f.Close()

	dest := filepath.Join(dir, "out", exeName())
	os.MkdirAll(filepath.Dir(dest), 0o755)
	if err := extract(zp, dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "binary" {
		t.Errorf("extracted %q", b)
	}
	if fi, _ := os.Stat(dest); fi.Mode()&0o111 == 0 {
		t.Error("extracted runtime isn't executable")
	}
}
