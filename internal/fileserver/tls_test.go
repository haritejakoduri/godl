package fileserver

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSelfSignedCertServesHTTPS(t *testing.T) {
	cert, err := SelfSignedCert([]string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()

	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("HTTPS request against the self-signed cert failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestSelfSignedCertAlwaysHasASubjectAltName(t *testing.T) {
	cert, err := SelfSignedCert(nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(leaf.IPAddresses) == 0 && len(leaf.DNSNames) == 0 {
		t.Fatal("a cert with no hosts requested should still fall back to a loopback SAN, not ship with none")
	}
}
