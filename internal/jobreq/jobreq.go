// Package jobreq resolves where a new download lands, the same way for
// every front end — the CLI's -o flag, the TUI's wizard and the web
// interface's form all mean one thing by "output".
package jobreq

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"godl/internal/paths"
)

// OutputPath resolves an output setting to an absolute path. Empty
// defaults to the user's Downloads folder; name, when given, is the
// filename to place inside it (a job that downloads a single file
// passes one, a job that downloads into a directory doesn't).
func OutputPath(output, name string) (string, error) {
	if output == "" {
		dir, err := paths.DownloadsDir()
		if err != nil {
			return "", err
		}
		output = filepath.Join(dir, name)
	}
	return paths.ResolveOutput(output)
}

// URLOutputs picks each link's destination file. One link keeps the
// original meaning of output (a file path); several links treat it as
// the directory they all land in, each named from its own URL.
func URLOutputs(links []string, output string, nameOf func(string) string) ([]string, error) {
	if len(links) == 1 {
		out, err := OutputPath(output, nameOf(links[0]))
		return []string{out}, err
	}
	dir := output
	if dir == "" {
		d, err := paths.DownloadsDir()
		if err != nil {
			return nil, err
		}
		dir = d
	}
	dir, err := paths.ResolveOutput(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	outs := make([]string, len(links))
	for i, link := range links {
		outs[i] = filepath.Join(dir, UniqueName(nameOf(link), taken))
	}
	return outs, nil
}

// UniqueName returns name, or "name (2).ext", "name (3).ext", ... if an
// earlier file in the same batch already claimed it — two links whose
// URLs end in the same filename would otherwise write the same file.
func UniqueName(name string, taken map[string]bool) string {
	candidate := name
	ext := ""
	if i := strings.LastIndex(name, "."); i > 0 {
		ext = name[i:]
		name = name[:i]
	}
	for n := 2; taken[candidate]; n++ {
		candidate = fmt.Sprintf("%s (%d)%s", name, n, ext)
	}
	taken[candidate] = true
	return candidate
}
