// Package httpx builds the HTTP clients the rest of godl uses.
//
// The split between TransferClient and Client is the point: a
// whole-request deadline is right for fetching a release manifest and
// catastrophic for downloading a 40GB file.
package httpx

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

const (
	dialTimeout         = 30 * time.Second
	tlsHandshakeTimeout = 15 * time.Second
	// responseHeaderTimeout bounds the wait for response headers, not the
	// body — which is what makes it safe for downloads. A server that
	// accepts the connection then says nothing fails here; a slow
	// multi-hour transfer is untouched.
	responseHeaderTimeout = 60 * time.Second
	idleConnTimeout       = 90 * time.Second
	// Go's default is 2. A chunked download opens several ranged requests
	// to one host at once and a WebDAV walk fans out PROPFINDs the same
	// way, so the default meant a fresh TCP and TLS handshake per request.
	maxIdleConnsPerHost = 16
)

const (
	MetadataTimeout = 30 * time.Second
	// BinaryFetchTimeout covers yt-dlp, ffmpeg and godl releases — tens
	// of MB, and users on slow links still need them to land.
	BinaryFetchTimeout = 30 * time.Minute
)

// Transport returns a Transport suitable for bulk transfers.
// insecureSkipVerify is for self-signed servers; leave it false
// otherwise.
//
// Cloned from http.DefaultTransport rather than built from scratch: a
// bare &http.Transport{} silently loses HTTP/2 negotiation, proxy
// resolution from the environment, and the expect-continue timeout.
func Transport(insecureSkipVerify bool) *http.Transport {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = (&net.Dialer{
		Timeout:   dialTimeout,
		KeepAlive: 30 * time.Second,
	}).DialContext
	tr.TLSHandshakeTimeout = tlsHandshakeTimeout
	tr.ResponseHeaderTimeout = responseHeaderTimeout
	tr.IdleConnTimeout = idleConnTimeout
	tr.MaxIdleConnsPerHost = maxIdleConnsPerHost
	if insecureSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	return tr
}

// TransferClient returns a client for downloading file content. It
// deliberately sets no Client.Timeout: that caps the whole request
// including the body, killing any download slower than the cap.
func TransferClient(insecureSkipVerify bool) *http.Client {
	return &http.Client{Transport: Transport(insecureSkipVerify)}
}

// Client returns a client for a short, bounded exchange — a manifest, a
// checksum, a probe — where a whole-request deadline is what's wanted.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{Transport: Transport(false), Timeout: timeout}
}
