// Package httpx builds the HTTP clients the rest of godl uses.
//
// It exists because every client in the codebase was previously either
// http.DefaultClient or a bare &http.Client{}: no timeouts anywhere, so
// a connection that opened and then went silent hung the caller
// forever, and — for the two clients that do bulk transfers — a
// hand-rolled Transport that quietly dropped HTTP/2 and kept Go's
// default cap of 2 idle connections per host while issuing eight
// concurrent requests to that same host.
//
// The split below is the important part. A whole-request deadline
// (http.Client.Timeout) is right for fetching a release manifest and
// catastrophic for downloading a 40GB file, so transfers get
// transport-level timeouts that bound *establishing* a connection and
// *waiting for a response*, while leaving the transfer itself unbounded.
package httpx

import (
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

const (
	// dialTimeout bounds getting a TCP connection up.
	dialTimeout = 30 * time.Second
	// tlsHandshakeTimeout bounds the TLS handshake on top of it.
	tlsHandshakeTimeout = 15 * time.Second
	// responseHeaderTimeout bounds how long a server may sit on a
	// request before sending any response headers. It does not bound the
	// body that follows, which is what makes it safe for downloads: a
	// server that accepts the connection and then says nothing fails
	// here instead of hanging, but a slow multi-hour transfer is
	// untouched.
	responseHeaderTimeout = 60 * time.Second
	// idleConnTimeout is how long a pooled connection is kept for reuse.
	idleConnTimeout = 90 * time.Second
)

// maxIdleConnsPerHost sizes the connection pool. Go's default is 2,
// which is actively wrong here: a chunked download opens several ranged
// requests to one host at once and a WebDAV walk fans out PROPFINDs the
// same way, so with the default all but two connections were closed
// after each request and re-established — a fresh TCP handshake and TLS
// negotiation per chunk, often the dominant cost of the operation.
const maxIdleConnsPerHost = 16

// Transport returns a Transport suitable for bulk transfers.
// insecureSkipVerify disables certificate verification, for the
// self-signed-server case; leave it false otherwise.
//
// Cloned from http.DefaultTransport rather than built from scratch so
// the settings that are easy to forget — HTTP/2 negotiation, proxy
// resolution from the environment, the expect-continue timeout — come
// along for free. Building a bare &http.Transport{} silently loses all
// of them.
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

// TransferClient returns a client for downloading file content: pooled,
// with the transport-level timeouts above, and deliberately *no*
// Client.Timeout, since that would cap the whole request including the
// body and kill any download slower than the cap.
func TransferClient(insecureSkipVerify bool) *http.Client {
	return &http.Client{Transport: Transport(insecureSkipVerify)}
}

// Client returns a client for a short, bounded exchange — a release
// manifest, a checksum, a metadata probe. Here a whole-request deadline
// is exactly what's wanted: the response is small, so if the whole thing
// hasn't arrived within timeout something is wrong.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{Transport: Transport(false), Timeout: timeout}
}

// Timeouts for the small fetches, named so call sites read as intent
// rather than as magic numbers.
const (
	// MetadataTimeout covers a JSON API response or an HTTP probe.
	MetadataTimeout = 30 * time.Second
	// BinaryFetchTimeout covers downloading a helper binary (yt-dlp,
	// ffmpeg, a godl release). Generous: these run to tens of MB and
	// users on slow links still need them to land.
	BinaryFetchTimeout = 30 * time.Minute
)
