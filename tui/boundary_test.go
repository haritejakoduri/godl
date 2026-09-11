package tui_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestNothingDependsOnTheTUI is the architectural guarantee this package
// exists to provide: presentation depends on the core, never the other
// way round. As long as that holds, a second front end (a GUI) can sit
// beside this package and drive the same internal packages, without
// either front end knowing the other exists.
//
// Only package cmd may import tui, and only to attach Run to the
// command tree. An internal package importing it would mean core logic
// had grown a dependency on the terminal, which is exactly the knot this
// layout was untied to avoid — and it's the kind of thing that creeps
// back in one convenient helper at a time, hence a test rather than a
// comment.
func TestNothingDependsOnTheTUI(t *testing.T) {
	// TestImports/XTestImports as well as Imports: a package's _test.go
	// files reaching for the TUI is the same coupling, and it's the
	// easier one to add by accident.
	out, err := exec.Command("go", "list", "-f",
		"{{.ImportPath}} {{join .Imports \" \"}} {{join .TestImports \" \"}} {{join .XTestImports \" \"}}",
		"godl/...").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}

	const allowed = "godl/cmd"
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pkg, imports, found := strings.Cut(line, " ")
		if !found || pkg == "godl/tui" || pkg == allowed {
			continue
		}
		for _, imp := range strings.Fields(imports) {
			if imp == "godl/tui" {
				t.Errorf("%s imports godl/tui — only %s may, or the core has grown a dependency on the terminal UI", pkg, allowed)
			}
		}
	}
}

// TestTUIDoesNotImportCmd is the same rule from the other side: the
// cobra command tree is one caller of this package, not a dependency of
// it. Importing cmd here would make the two mutually dependent and leave
// no seam for a GUI to slot into.
func TestTUIDoesNotImportCmd(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", "{{join .Imports \"\\n\"}}", "godl/tui").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, imp := range strings.Fields(string(out)) {
		if imp == "godl/cmd" {
			t.Error("godl/tui imports godl/cmd — the front end must not depend on the command tree that invokes it")
		}
	}
}
