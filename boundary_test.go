package main_test

import (
	"os/exec"
	"strings"
	"testing"
)

// frontEnds are godl's presentation layers. Each is a leaf: package cmd
// attaches its Run to the command tree, and nothing else may touch it.
var frontEnds = []string{"godl/tui", "godl/tray"}

// TestNothingDependsOnAFrontEnd is the architectural guarantee those
// packages exist to provide: presentation depends on the core, never
// the other way round. As long as that holds, front ends sit beside one
// another driving the same internal packages, with none of them aware
// the others exist — which is what let the tray be added without the
// TUI or the core knowing about it.
//
// An internal package importing one would mean core logic had grown a
// dependency on a particular interface, which is exactly the knot this
// layout was untied to avoid — and it's the kind of thing that creeps
// back in one convenient helper at a time, hence a test rather than a
// comment.
func TestNothingDependsOnAFrontEnd(t *testing.T) {
	// TestImports/XTestImports as well as Imports: a package's _test.go
	// files reaching for a front end is the same coupling, and it's the
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
		if !found || pkg == allowed || isFrontEnd(pkg) {
			continue
		}
		for _, imp := range strings.Fields(imports) {
			if isFrontEnd(imp) {
				t.Errorf("%s imports %s — only %s may, or the core has grown a dependency on a front end", pkg, imp, allowed)
			}
		}
	}
}

// TestFrontEndsDoNotImportCmd is the same rule from the other side: the
// cobra command tree is one caller of these packages, not a dependency
// of them. Importing cmd would make the two mutually dependent and
// leave no seam for another front end to slot into.
func TestFrontEndsDoNotImportCmd(t *testing.T) {
	for _, fe := range frontEnds {
		out, err := exec.Command("go", "list", "-f", "{{join .Imports \"\\n\"}}", fe).Output()
		if err != nil {
			t.Skipf("go list unavailable: %v", err)
		}
		for _, imp := range strings.Fields(string(out)) {
			if imp == "godl/cmd" {
				t.Errorf("%s imports godl/cmd — a front end must not depend on the command tree that invokes it", fe)
			}
		}
	}
}

// TestFrontEndsDoNotImportEachOther keeps them peers rather than a
// chain: a tray that reached into the TUI for a helper would make the
// terminal UI a dependency of the desktop one, so neither could be
// built or replaced alone.
func TestFrontEndsDoNotImportEachOther(t *testing.T) {
	for _, fe := range frontEnds {
		out, err := exec.Command("go", "list", "-f", "{{join .Imports \"\\n\"}}", fe).Output()
		if err != nil {
			t.Skipf("go list unavailable: %v", err)
		}
		for _, imp := range strings.Fields(string(out)) {
			if isFrontEnd(imp) && imp != fe {
				t.Errorf("%s imports %s — front ends must stay peers, not a chain", fe, imp)
			}
		}
	}
}

func isFrontEnd(pkg string) bool {
	for _, fe := range frontEnds {
		if pkg == fe {
			return true
		}
	}
	return false
}
