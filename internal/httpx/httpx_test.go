package httpx

import (
	"net/http"
	"testing"
)

// TestTransportKeepsDefaultsWorthKeeping guards the reason Transport
// clones http.DefaultTransport instead of building a bare
// &http.Transport{}: the bare form silently drops HTTP/2 negotiation and
// environment proxy support, which is exactly how the WebDAV client lost
// both without anyone noticing.
func TestTransportKeepsDefaultsWorthKeeping(t *testing.T) {
	tr := Transport(false)
	if !tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 is false — a hand-built Transport loses HTTP/2; clone http.DefaultTransport instead")
	}
	if tr.Proxy == nil {
		t.Error("Proxy is nil — HTTP(S)_PROXY would be ignored")
	}
}

// TestTransportSizesThePool covers the other half: Go's default of two
// idle connections per host throttles a chunked download or a WebDAV
// walk, both of which issue several concurrent requests to one host and
// would otherwise re-handshake most of them every time.
func TestTransportSizesThePool(t *testing.T) {
	tr := Transport(false)
	if tr.MaxIdleConnsPerHost < 8 {
		t.Errorf("MaxIdleConnsPerHost = %d, want room for concurrent chunks/PROPFINDs (Go's default of 2 forces a reconnect per request)", tr.MaxIdleConnsPerHost)
	}
	if tr.ResponseHeaderTimeout == 0 {
		t.Error("ResponseHeaderTimeout is unset — a server that accepts a connection then says nothing would hang forever")
	}
	if tr.TLSHandshakeTimeout == 0 {
		t.Error("TLSHandshakeTimeout is unset")
	}
}

// TestTransferClientHasNoWholeRequestDeadline is the distinction that
// matters most in this package. http.Client.Timeout covers the response
// body too, so putting one on a download client caps how long a download
// may take — a 40GB file over a slow link would be severed mid-transfer.
// Transfers must be bounded by the transport's connect/header timeouts
// and their own idle watchdogs instead.
func TestTransferClientHasNoWholeRequestDeadline(t *testing.T) {
	if c := TransferClient(false); c.Timeout != 0 {
		t.Errorf("TransferClient has Timeout=%s, want 0 — a whole-request deadline kills long downloads", c.Timeout)
	}
}

func TestClientHasAWholeRequestDeadline(t *testing.T) {
	if c := Client(MetadataTimeout); c.Timeout != MetadataTimeout {
		t.Errorf("Client(MetadataTimeout).Timeout = %s, want %s", c.Timeout, MetadataTimeout)
	}
}

func TestTransportInsecureSkipVerify(t *testing.T) {
	if tr := Transport(false); tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("Transport(false) skips certificate verification")
	}
	tr := Transport(true)
	if tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify {
		t.Error("Transport(true) does not skip certificate verification")
	}
}

// Transport must hand back a fresh instance each call: callers mutate
// what they get (the self-signed case sets TLSClientConfig), and sharing
// one would leak that across every client in the process.
func TestTransportIsNotShared(t *testing.T) {
	a, b := Transport(false), Transport(false)
	if a == b {
		t.Fatal("Transport returned the same instance twice — one caller's TLS override would affect every other client")
	}
	var _ *http.Transport = a
}
