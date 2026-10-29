package webcert

import (
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func leafOf(t *testing.T, cert tls.Certificate) *x509.Certificate {
	t.Helper()
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

// TestLoadGeneratesAUsableCertificate covers the shape a browser
// actually checks: it has to be valid now, carry server auth, and name
// the addresses someone will reach it by. A certificate missing any of
// those is refused by the browser, not by godl, so nothing here would
// fail at runtime — the page would just never load.
func TestLoadGeneratesAUsableCertificate(t *testing.T) {
	dir := t.TempDir()
	cert, pemBytes, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	leaf := leafOf(t, cert)

	if time.Now().Before(leaf.NotBefore) || time.Now().After(leaf.NotAfter) {
		t.Errorf("certificate is not valid now: %s to %s", leaf.NotBefore, leaf.NotAfter)
	}
	// Browsers cap a certificate's life; a year stays under that.
	if life := leaf.NotAfter.Sub(leaf.NotBefore); life > 398*24*time.Hour {
		t.Errorf("certificate lasts %s, longer than the 398 days browsers accept", life)
	}
	var serverAuth bool
	for _, u := range leaf.ExtKeyUsage {
		if u == x509.ExtKeyUsageServerAuth {
			serverAuth = true
		}
	}
	if !serverAuth {
		t.Error("certificate has no ServerAuth extended key usage, so a browser will not accept it for TLS")
	}
	// Loopback matters as much as the LAN address: the machine running
	// godl reaches its own interface at 127.0.0.1.
	var hasLoopback bool
	for _, ip := range leaf.IPAddresses {
		if ip.IsLoopback() {
			hasLoopback = true
		}
	}
	if !hasLoopback {
		t.Errorf("certificate names %v, none of them loopback", leaf.IPAddresses)
	}
	if !strings.Contains(string(pemBytes), "BEGIN CERTIFICATE") {
		t.Error("returned bytes are not a PEM certificate, so the download would be unusable")
	}

	// The private key must not be readable by other users on the
	// machine: anything that can read it can impersonate the page.
	info, err := os.Stat(filepath.Join(dir, keyFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("private key mode is %o, want no group or other access", perm)
	}
}

// TestLoadReusesTheStoredCertificate: a new one every start would mean
// every device has to trust it again every time, which would make the
// feature unusable without anything looking broken.
func TestLoadReusesTheStoredCertificate(t *testing.T) {
	dir := t.TempDir()
	first, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !leafOf(t, first).Equal(leafOf(t, second)) {
		t.Error("Load returned a different certificate the second time; every device would have to trust it again")
	}
}

// TestLoadReplacesAnExpiringCertificate: a daemon left running for a
// year would otherwise start serving an expired certificate, which
// every browser rejects outright, with nothing on godl's side looking
// wrong.
func TestLoadReplacesAnExpiringCertificate(t *testing.T) {
	dir := t.TempDir()

	// One that is already inside the renewal window when written.
	restore := validFor
	validFor = renewBefore / 2
	first, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	validFor = restore

	second, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if leafOf(t, first).Equal(leafOf(t, second)) {
		t.Fatal("Load kept a certificate inside its renewal window; it would eventually serve an expired one")
	}
	if got := leafOf(t, second).NotAfter; time.Now().Add(renewBefore).After(got) {
		t.Errorf("replacement expires at %s, still inside the renewal window", got)
	}
}

// TestLoadReplacesACertificateMissingAnAddress is the laptop that
// moved to another network: the stored certificate never promised the
// address the machine now has, so a browser reaching it there rejects
// it. Nothing fails on godl's side — the page simply will not load.
func TestLoadReplacesACertificateMissingAnAddress(t *testing.T) {
	dir := t.TempDir()

	restore := hostAddrs
	defer func() { hostAddrs = restore }()

	hostAddrs = func() []net.IP { return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.168.1.10")} }
	first, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Same machine, different network.
	hostAddrs = func() []net.IP { return []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("10.0.0.5")} }
	second, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if leafOf(t, first).Equal(leafOf(t, second)) {
		t.Fatal("Load kept a certificate that does not name this machine's address")
	}
	if !covers(leafOf(t, second), []net.IP{net.ParseIP("10.0.0.5")}) {
		t.Error("the replacement still does not name the new address")
	}
}

// TestCoversRejectsAnUnknownAddress is the laptop-moved-network case:
// a certificate made on one network does not name the address the
// machine has on another, and a browser reaching it there rejects it.
func TestCoversRejectsAnUnknownAddress(t *testing.T) {
	leaf := &x509.Certificate{IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("192.168.1.10")}}

	if !covers(leaf, []net.IP{net.ParseIP("192.168.1.10")}) {
		t.Error("covers() said no for an address the certificate names")
	}
	if covers(leaf, []net.IP{net.ParseIP("10.0.0.5")}) {
		t.Error("covers() said yes for an address the certificate does not name")
	}
}

// TestFingerprintIsReadable: it is shown so someone can compare it
// with what their phone displays before trusting the certificate, so
// it has to match that format — uppercase hex, colon separated.
func TestFingerprintIsReadable(t *testing.T) {
	dir := t.TempDir()
	cert, _, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	fp := Fingerprint(cert)
	if n := strings.Count(fp, ":"); n != 31 {
		t.Errorf("fingerprint %q has %d colons, want 31 for a 32-byte SHA-256", fp, n)
	}
	if strings.ToUpper(fp) != fp {
		t.Errorf("fingerprint %q is not uppercase, which is how browsers show it", fp)
	}
	if Fingerprint(tls.Certificate{}) != "" {
		t.Error("Fingerprint of an empty certificate should be empty, not panic or invent one")
	}
}
