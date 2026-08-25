package cmd

import (
	"net"
	"strconv"
	"testing"
)

func TestIsLoopbackHost(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"localhost", true},
		{"127.0.0.1", true},
		{"::1", true},
		{"0.0.0.0", false},
		{"192.168.1.5", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isLoopbackHost(c.host); got != c.want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", c.host, got, c.want)
		}
	}
}

// TestValidateServeFlagsRefusesUnauthenticatedNonLoopback is a
// regression test for the central safety rail of "godl serve": binding
// to a reachable address without any authentication (and without an
// explicit override) must be refused outright, not just warned about —
// exposing a directory to the whole LAN by mistake is the one mistake
// this command can make that isn't easily undone (anyone who noticed
// the window could have already copied files off it).
func TestValidateServeFlagsRefusesUnauthenticatedNonLoopback(t *testing.T) {
	if err := validateServeFlags("/some/dir", "0.0.0.0", false, false, "", "", false); err == nil {
		t.Fatal("serving 0.0.0.0 with no auth and no override should be refused")
	}
}

func TestValidateServeFlagsAllowsLoopbackWithoutAuth(t *testing.T) {
	if err := validateServeFlags("/some/dir", "127.0.0.1", false, false, "", "", false); err != nil {
		t.Errorf("loopback-only without auth should be allowed: %v", err)
	}
}

func TestValidateServeFlagsAllowsAuthedNonLoopback(t *testing.T) {
	if err := validateServeFlags("/some/dir", "0.0.0.0", true, false, "", "", false); err != nil {
		t.Errorf("authenticated non-loopback should be allowed: %v", err)
	}
}

func TestValidateServeFlagsAllowsExplicitOverride(t *testing.T) {
	if err := validateServeFlags("/some/dir", "0.0.0.0", false, true, "", "", false); err != nil {
		t.Errorf("--insecure-no-auth should override the refusal: %v", err)
	}
}

func TestValidateServeFlagsRejectsBothTLSModes(t *testing.T) {
	if err := validateServeFlags("/some/dir", "127.0.0.1", false, false, "cert.pem", "key.pem", true); err == nil {
		t.Fatal("--tls-cert together with --self-signed should be rejected")
	}
}

func TestValidateServeFlagsRequiresTLSCertAndKeyTogether(t *testing.T) {
	if err := validateServeFlags("/some/dir", "127.0.0.1", false, false, "cert.pem", "", false); err == nil {
		t.Fatal("--tls-cert without --tls-key should be rejected")
	}
	if err := validateServeFlags("/some/dir", "127.0.0.1", false, false, "", "key.pem", false); err == nil {
		t.Fatal("--tls-key without --tls-cert should be rejected")
	}
}

func TestListenWithFallbackUsesRequestedPortWhenFree(t *testing.T) {
	// Grab an OS-assigned free port, then release it immediately so
	// listenWithFallback gets a fair shot at binding it itself.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	l, actual, err := listenWithFallback("127.0.0.1", port, 5)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if actual != port {
		t.Errorf("actual port = %d, want the requested %d (it was free)", actual, port)
	}
}

// TestListenWithFallbackSkipsOccupiedPort is the regression test for
// the actual feature: asking for a port that's already taken must land
// on a different, real, working port instead of failing outright.
func TestListenWithFallbackSkipsOccupiedPort(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	l, actual, err := listenWithFallback("127.0.0.1", port, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if actual == port {
		t.Fatal("should not have reused the already-occupied port")
	}
	if actual <= port {
		t.Errorf("actual port = %d, want something greater than the occupied %d", actual, port)
	}
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(actual)))
	if err != nil {
		t.Errorf("fallback port %d isn't actually accepting connections: %v", actual, err)
	} else {
		conn.Close()
	}
}

func TestListenWithFallbackGivesUpAfterMaxAttempts(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	// Only 1 attempt allowed, and that one port is occupied — no room
	// to fall back to anything.
	if _, _, err := listenWithFallback("127.0.0.1", port, 1); err == nil {
		t.Fatal("expected an error when the only allowed port is occupied")
	}
}

func TestIsAddrInUseErr(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port

	_, conflictErr := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if conflictErr == nil {
		t.Fatal("expected a real bind conflict to test against")
	}
	if !isAddrInUseErr(conflictErr) {
		t.Errorf("isAddrInUseErr(%v) = false, want true for a real address-in-use conflict", conflictErr)
	}
	if isAddrInUseErr(nil) {
		t.Error("isAddrInUseErr(nil) should be false")
	}
}
