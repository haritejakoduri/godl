package tui

import (
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestNewServeFormDefaultsMatchTheCLI(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GODL_DOWNLOADS_DIR", "")
	s := newServeForm()
	if s.host != "0.0.0.0" || s.port != "8080" {
		t.Errorf("host=%q port=%q, want the same defaults as \"godl serve\" (0.0.0.0:8080)", s.host, s.port)
	}
	if s.allowWrite || s.selfSigned || s.insecureNoAuth {
		t.Error("a fresh form should default to read-only, plain HTTP, auth-required — same as the CLI")
	}
}

// TestServeRejectsNonLoopbackWithoutAuth is the TUI-side regression
// test for the same safety rail cmd/serve.go's validateServeFlags
// enforces: binding somewhere reachable without a username/password
// must be refused rather than silently exposing the directory.
func TestServeRejectsNonLoopbackWithoutAuth(t *testing.T) {
	s := &serveState{dir: t.TempDir(), host: "0.0.0.0", port: "0"}
	m := statusModel{serve: s}
	mm, _ := m.startServe()
	m = mm.(statusModel)
	if m.serve == nil || m.serve.step != serveEditing || m.serve.err == "" {
		t.Fatal("serving 0.0.0.0 with no username/password should be refused, staying on the form with an error")
	}
}

// TestServeInsecureNoAuthOverridesTheRefusal mirrors the CLI's
// --insecure-no-auth flag (TestValidateServeFlagsAllowsExplicitOverride
// in cmd/serve_test.go): with the toggle on, the same non-loopback,
// no-auth combination that's normally refused is allowed to start.
func TestServeInsecureNoAuthOverridesTheRefusal(t *testing.T) {
	s := &serveState{dir: t.TempDir(), host: "0.0.0.0", port: "0", insecureNoAuth: true}
	m := statusModel{serve: s}
	mm, _ := m.startServe()
	m = mm.(statusModel)
	if m.serve == nil || m.serve.step != serveRunning {
		t.Fatalf("insecureNoAuth should override the refusal; err=%q", s.err)
	}
	m.stopServe()
}

// TestServeAllowsLoopbackWithoutAuthAndStops is the positive case: a
// loopback host needs no auth, actually starts listening, and can be
// stopped cleanly (freeing the port) via the same key that starts it.
func TestServeAllowsLoopbackWithoutAuthAndStops(t *testing.T) {
	s := &serveState{dir: t.TempDir(), host: "127.0.0.1", port: "0"}
	m := statusModel{serve: s}
	mm, _ := m.startServe()
	m = mm.(statusModel)
	if m.serve == nil || m.serve.step != serveRunning {
		t.Fatalf("loopback with no auth should be allowed to start; err=%q", s.err)
	}
	if m.serve.srv == nil {
		t.Fatal("a running server should have its *http.Server recorded")
	}

	// The server is actually accepting connections, not just marked
	// "running" in the model.
	addr := m.serve.srv.Addr
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("server isn't actually listening on %s: %v", addr, err)
	}
	conn.Close()

	mm, _ = m.stopServe()
	m = mm.(statusModel)
	if m.serve != nil {
		t.Fatal("stopServe should close the overlay")
	}
	if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		t.Error("the port should be freed once stopped")
	}
}

// TestServeAllowsAuthedNonLoopback mirrors
// TestValidateServeFlagsAllowsAuthedNonLoopback in cmd/serve_test.go:
// a username+password pair is enough to serve a reachable address.
func TestServeAllowsAuthedNonLoopback(t *testing.T) {
	s := &serveState{dir: t.TempDir(), host: "0.0.0.0", port: "0", username: "alice", password: "secret"}
	m := statusModel{serve: s}
	mm, _ := m.startServe()
	m = mm.(statusModel)
	if m.serve == nil || m.serve.step != serveRunning {
		t.Fatalf("0.0.0.0 with a username/password should be allowed; err=%q", s.err)
	}
	m.stopServe()
}

func TestServeRejectsAMissingDirectory(t *testing.T) {
	s := &serveState{dir: "/does/not/exist/anywhere", host: "127.0.0.1", port: "0"}
	m := statusModel{serve: s}
	mm, _ := m.startServe()
	m = mm.(statusModel)
	if m.serve.step != serveEditing || m.serve.err == "" {
		t.Fatal("a nonexistent directory should be refused with an error, not started")
	}
}

func TestServeRejectsABadPort(t *testing.T) {
	s := &serveState{dir: t.TempDir(), host: "127.0.0.1", port: "not-a-number"}
	m := statusModel{serve: s}
	mm, _ := m.startServe()
	m = mm.(statusModel)
	if m.serve.step != serveEditing || m.serve.err == "" {
		t.Fatal("a non-numeric port should be refused with an error, not started")
	}
}

// TestServeFormFieldNavigationBounds mirrors the Settings tab's own
// cursor-clamping test: up/down should never go out of range.
func TestServeFormFieldNavigationBounds(t *testing.T) {
	m := statusModel{serve: newServeForm()}
	mm, _ := m.updateServeForm(key("up"))
	m = mm.(statusModel)
	if m.serve.cursor != serveFieldDir {
		t.Fatalf("up at the top should stay put, got %d", m.serve.cursor)
	}
	for i := 0; i < int(serveFieldStart)+3; i++ {
		mm, _ = m.updateServeForm(key("down"))
		m = mm.(statusModel)
	}
	if m.serve.cursor != serveFieldStart {
		t.Fatalf("cursor after overshooting down = %d, want %d (clamped)", m.serve.cursor, serveFieldStart)
	}
}

func TestServeFormAllowWriteToggles(t *testing.T) {
	m := statusModel{serve: newServeForm()}
	m.serve.cursor = serveFieldAllowWrite
	mm, _ := m.updateServeForm(key("enter"))
	m = mm.(statusModel)
	if !m.serve.allowWrite {
		t.Fatal("enter on Allow write should toggle it on")
	}
}

func TestServeFormInsecureNoAuthToggles(t *testing.T) {
	m := statusModel{serve: newServeForm()}
	m.serve.cursor = serveFieldInsecureNoAuth
	mm, _ := m.updateServeForm(key("enter"))
	m = mm.(statusModel)
	if !m.serve.insecureNoAuth {
		t.Fatal("enter on the Insecure field should toggle it on")
	}
	mm, _ = m.updateServeForm(key(" "))
	m = mm.(statusModel)
	if m.serve.insecureNoAuth {
		t.Fatal("space should toggle it back off")
	}
}

// TestServeRunningOnlyRespondsToStopKeys checks a running server
// ignores everything except esc/x — a stray keypress shouldn't tear
// the server down, but esc/x deliberately always does (see serveState's
// own doc comment on why there's no "leave it running" option).
func TestServeRunningOnlyRespondsToStopKeys(t *testing.T) {
	s := &serveState{dir: t.TempDir(), host: "127.0.0.1", port: "0"}
	m := statusModel{serve: s}
	mm, _ := m.startServe()
	m = mm.(statusModel)

	mm, _ = m.updateServeRunning(key("q"))
	m = mm.(statusModel)
	if m.serve == nil {
		t.Fatal("an unrelated key should not stop the server")
	}

	mm, _ = m.updateServeRunning(key("x"))
	m = mm.(statusModel)
	if m.serve != nil {
		t.Fatal("x should stop the server and close the overlay")
	}
}

// TestServeBannerLinesMentionTheDirectoryAndWebDAV is a light
// regression test that the banner (mirroring cmd/serve.go's own
// printServeBanner) still carries the pieces users read it for.
func TestServeBannerLinesMentionTheDirectoryAndWebDAV(t *testing.T) {
	lines := serveBannerLines("/srv/public", "192.168.1.50", 8080, false, false, "", false)
	joined := ""
	for _, l := range lines {
		joined += l + "\n"
	}
	for _, want := range []string{"/srv/public", "192.168.1.50:8080", "/dav/", "No authentication"} {
		if !strings.Contains(joined, want) {
			t.Errorf("banner lines missing %q:\n%s", want, joined)
		}
	}
}

// Sanity check that fileserver-backed Config actually rejects a
// non-GET write when ReadOnly, exercised through the same startServe
// path the TUI uses — a regression guard that wiring Config.ReadOnly
// to the "Allow write" toggle didn't get inverted.
func TestServeReadOnlyByDefaultRejectsWrites(t *testing.T) {
	s := &serveState{dir: t.TempDir(), host: "127.0.0.1", port: "0"}
	m := statusModel{serve: s}
	mm, _ := m.startServe()
	m = mm.(statusModel)
	if m.serve == nil || m.serve.step != serveRunning {
		t.Fatalf("expected the server to start; err=%q", s.err)
	}
	defer m.stopServe()

	req, err := http.NewRequest(http.MethodPut, "http://"+m.serve.srv.Addr+"/dav/new.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed && resp.StatusCode != http.StatusForbidden {
		t.Errorf("PUT against a read-only server = %d, want it rejected", resp.StatusCode)
	}
}
