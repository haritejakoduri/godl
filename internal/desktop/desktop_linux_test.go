//go:build linux

package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// inTempHome points XDG_DATA_HOME at a throwaway directory so these
// tests never touch the real application menu.
func inTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dir)
	return dir
}

func TestInstallWritesEntryAndIcons(t *testing.T) {
	dir := inTempHome(t)

	if err := Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}

	entry := filepath.Join(dir, "applications", Name+".desktop")
	body, err := os.ReadFile(entry)
	if err != nil {
		t.Fatalf("reading the desktop entry: %v", err)
	}
	for _, want := range []string{
		"Type=Application",
		"Name=godl",
		"Icon=" + Name,
		"Terminal=false",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("desktop entry missing %q:\n%s", want, body)
		}
	}

	// Icon=godl only resolves if the theme actually holds a godl.png
	// at the sizes a launcher looks in, so check the files rather than
	// trusting that the key was written.
	for _, size := range []string{"16x16", "48x48", "256x256"} {
		p := filepath.Join(dir, "icons", "hicolor", size, "apps", Name+".png")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("no icon at %s: %v", size, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "icons", "hicolor", "scalable", "apps", Name+".svg")); err != nil {
		t.Errorf("no scalable icon: %v", err)
	}
}

// TestInstalledEntryPointsAtARealBinary guards the thing that makes an
// entry useless rather than absent: a launcher silently does nothing
// when Exec names a path that isn't there.
func TestInstalledEntryPointsAtARealBinary(t *testing.T) {
	dir := inTempHome(t)
	if err := Install(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "applications", Name+".desktop"))
	if err != nil {
		t.Fatal(err)
	}

	var exec string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "Exec=") {
			exec = strings.TrimPrefix(line, "Exec=")
		}
	}
	if exec == "" {
		t.Fatal("no Exec= line in the desktop entry")
	}
	bin := strings.Fields(exec)[0]
	if !filepath.IsAbs(bin) {
		t.Errorf("Exec names %q, which is not an absolute path — a launcher has no $PATH to resolve it against", bin)
	}
	if _, err := os.Stat(bin); err != nil {
		t.Errorf("Exec points at %q, which does not exist: %v", bin, err)
	}
}

// TestRemoveLeavesOtherApplicationsAlone: the icon directories are
// shared with every other installed application, so removal must take
// godl's files and nothing else.
func TestRemoveLeavesOtherApplicationsAlone(t *testing.T) {
	dir := inTempHome(t)
	if err := Install(); err != nil {
		t.Fatal(err)
	}

	neighbour := filepath.Join(dir, "icons", "hicolor", "48x48", "apps", "someone-else.png")
	if err := os.WriteFile(neighbour, []byte("not ours"), 0o644); err != nil {
		t.Fatal(err)
	}
	otherApp := filepath.Join(dir, "applications", "someone-else.desktop")
	if err := os.WriteFile(otherApp, []byte("[Desktop Entry]"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	for _, p := range []string{neighbour, otherApp} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("Remove deleted another application's file %s: %v", filepath.Base(p), err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "icons", "hicolor", "48x48", "apps")); err != nil {
		t.Errorf("Remove deleted a shared icon directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "applications", Name+".desktop")); !os.IsNotExist(err) {
		t.Errorf("godl's own entry survived Remove: %v", err)
	}
}

func TestRemoveWithNothingInstalledIsNotAnError(t *testing.T) {
	inTempHome(t)
	if err := Remove(); err != nil {
		t.Errorf("Remove with nothing installed = %v, want nil", err)
	}
}

func TestStatusTracksInstallAndRemove(t *testing.T) {
	inTempHome(t)

	if installed, _, err := Status(); err != nil || installed {
		t.Fatalf("Status before install = (%v, %v), want (false, nil)", installed, err)
	}
	if err := Install(); err != nil {
		t.Fatal(err)
	}
	if installed, where, err := Status(); err != nil || !installed {
		t.Fatalf("Status after install = (%v, %q, %v), want installed", installed, where, err)
	}
	if err := Remove(); err != nil {
		t.Fatal(err)
	}
	if installed, _, err := Status(); err != nil || installed {
		t.Fatalf("Status after remove = (%v, %v), want (false, nil)", installed, err)
	}
}

// TestInstallIsIdempotent: installing over an existing entry is how a
// moved or upgraded binary gets its path corrected, so it has to be a
// normal operation rather than an error.
func TestInstallIsIdempotent(t *testing.T) {
	dir := inTempHome(t)
	for i := 0; i < 3; i++ {
		if err := Install(); err != nil {
			t.Fatalf("Install #%d: %v", i+1, err)
		}
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "applications", "*.desktop"))
	if len(matches) != 1 {
		t.Errorf("after three installs there are %d desktop entries, want 1: %v", len(matches), matches)
	}
}
