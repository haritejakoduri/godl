// Package webcert makes the certificate the web interface serves over
// the network with.
//
// It is self-signed, because the alternative is a real one, and a real
// one needs a domain name and a public CA — neither of which a program
// that lives on your home network has. The cost is that a browser does
// not trust it until the person tells it to, once per device.
//
// What that buys is the thing plain http cannot do. Over the network
// godl authenticates with HTTP Basic, which is a username and password
// in a header in the clear, readable by anything on the same Wi-Fi;
// TLS closes that. It also makes the page a secure context, which is
// what a phone's browser requires before it will install the interface
// as an app.
package webcert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// File names under the data directory, beside webui.token.
const (
	certFile = "webui-cert.pem"
	keyFile  = "webui-key.pem"
)

// validFor is how long a generated certificate lasts, and renewBefore
// how close to the end is close enough to replace it early. A year is
// chosen to stay under the 398 days browsers cap a certificate at;
// renewing a month ahead means a long-running daemon never serves an
// expired one.
const renewBefore = 30 * 24 * time.Hour

// validFor is a var, not a const, so a test can make a certificate
// that is already expiring and check that Load replaces it — the
// alternative being a test that waits a year.
var validFor = 365 * 24 * time.Hour

// hostAddrs is likewise replaceable, so a test can play the
// laptop-moved-to-another-network case without having another
// network.
var hostAddrs = realHostAddrs

// Load returns the certificate to serve with, generating one if there
// is none, if the stored one is expiring, or if this machine has an
// address the stored one doesn't cover. The second return is the
// certificate in PEM form, which is what a phone installs to trust it.
func Load(dataDir string) (tls.Certificate, []byte, error) {
	certPath := filepath.Join(dataDir, certFile)
	keyPath := filepath.Join(dataDir, keyFile)

	pemBytes, err := os.ReadFile(certPath)
	if err == nil {
		if cert, ok := usable(certPath, keyPath, pemBytes, hostAddrs()); ok {
			return cert, pemBytes, nil
		}
	} else if !os.IsNotExist(err) {
		return tls.Certificate{}, nil, err
	}
	return generate(certPath, keyPath, hostAddrs())
}

// usable reports whether the stored pair can still be served. Anything
// wrong with it — unreadable, expiring, missing one of this machine's
// addresses — means generating a new one, which is always safe: the
// certificate carries no state worth keeping.
func usable(certPath, keyPath string, pemBytes []byte, addrs []net.IP) (tls.Certificate, bool) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, false
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, false
	}
	if time.Now().Add(renewBefore).After(leaf.NotAfter) {
		return tls.Certificate{}, false
	}
	// A laptop that moved to another network has an address the old
	// certificate never promised, and a browser reaching it by that
	// address would reject the certificate rather than godl.
	if !covers(leaf, addrs) {
		return tls.Certificate{}, false
	}
	cert.Leaf = leaf
	return cert, true
}

func covers(leaf *x509.Certificate, addrs []net.IP) bool {
	for _, ip := range addrs {
		found := false
		for _, have := range leaf.IPAddresses {
			if have.Equal(ip) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// generate writes a fresh self-signed certificate and its key.
func generate(certPath, keyPath string, addrs []net.IP) (tls.Certificate, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, nil, err
	}

	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "godl", Organization: []string{"godl"}},
		// Backdated an hour so a device whose clock runs slow doesn't
		// reject a certificate made seconds ago.
		NotBefore:   now.Add(-time.Hour),
		NotAfter:    now.Add(validFor),
		KeyUsage:    x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    []string{"localhost"},
		IPAddresses: addrs,
		// Its own issuer, so a device can be told to trust this one
		// certificate and have that cover the connection it signs.
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, nil, err
	}
	// The private key is the whole of the protection: anything that
	// can read it can impersonate this interface.
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, nil, err
	}

	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, certPEM, nil
}

// realHostAddrs is every address a browser might reach this machine by.
// Loopback is always included — the certificate has to work for the
// desktop too, which reaches the interface at 127.0.0.1.
func realHostAddrs() []net.IP {
	addrs := []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}
	ifaces, err := net.InterfaceAddrs()
	if err != nil {
		return addrs
	}
	for _, a := range ifaces {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
			continue
		}
		addrs = append(addrs, ipnet.IP)
	}
	return addrs
}

// Fingerprint is the certificate's SHA-256, in the colon-separated
// hex a browser shows when asked to inspect it — so someone installing
// it on a phone can check they are trusting the right one.
func Fingerprint(cert tls.Certificate) string {
	if len(cert.Certificate) == 0 {
		return ""
	}
	sum := sha256Sum(cert.Certificate[0])
	out := make([]byte, 0, len(sum)*3)
	const hex = "0123456789ABCDEF"
	for i, b := range sum {
		if i > 0 {
			out = append(out, ':')
		}
		out = append(out, hex[b>>4], hex[b&0x0f])
	}
	return string(out)
}

func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}
