// Package webdav is a minimal WebDAV client: enough to stat a remote
// path, list a directory's immediate children, and download a file with
// basic auth and Range-based resume. It intentionally doesn't implement
// the whole RFC 4918 — just what "godl webdav" needs to walk and pull a
// file or folder tree.
package webdav

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// Cloud-storage-proxying backends rate-limit hard enough that even a
// lone PROPFIND can be 429'd; without a retry that looks like a broken
// connection rather than the "back off a moment" it is.
const retry429Max = 5

// maxRetryDelay caps one wait, however long a Retry-After asks for: a
// download shouldn't hang that long, and retry429Max ends things if the
// server really is down.
const maxRetryDelay = 30 * time.Second

// Base of the exponential fallback when a 429 carries no Retry-After.
// Var so tests don't sleep through real backoff.
var retryBackoffUnit = time.Second

// doRetrying429 runs one round trip, retrying on 429 — honoring
// Retry-After when present, else backing off exponentially. do is called
// afresh each attempt, since a request body can't be replayed.
func (c *Client) doRetrying429(ctx context.Context, do func() (*http.Response, error)) (*http.Response, error) {
	gate := gateFor(c.base.Host)
	var resp *http.Response
	var err error
	for attempt := 0; attempt < retry429Max; attempt++ {
		// The slot covers the request and its response headers, not the
		// body: what draws 429s is the rate of new requests, and holding
		// a slot through a multi-minute transfer would let one download
		// starve every PROPFIND behind it. See hostgate.go.
		release, aerr := gate.acquire(ctx)
		if aerr != nil {
			return nil, aerr
		}
		resp, err = do()
		release()
		if err != nil || resp.StatusCode != http.StatusTooManyRequests {
			return resp, err
		}
		delay, ok := retryAfterDelay(resp.Header.Get("Retry-After"))
		resp.Body.Close()
		if !ok {
			delay = time.Duration(1<<attempt) * retryBackoffUnit
		}
		if delay > maxRetryDelay {
			delay = maxRetryDelay
		}
		// Tell the gate before sleeping, so requests that haven't been
		// sent yet also hold off instead of walking into the same limit.
		gate.throttled(delay)
		select {
		case <-time.After(jitter(delay)):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return resp, err
}

// retryAfterDelay parses Retry-After's seconds form. ok is false only
// when the header is missing, negative, or in HTTP-date form. "0" is a
// legitimate value — retry immediately — and must not be read as
// "absent".
func retryAfterDelay(v string) (delay time.Duration, ok bool) {
	if v == "" {
		return 0, false
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs) * time.Second, true
}
