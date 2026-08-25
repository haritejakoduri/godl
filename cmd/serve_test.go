package cmd

import "testing"

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
