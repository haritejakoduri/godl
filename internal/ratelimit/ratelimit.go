// Package ratelimit parses the --limit-rate flag shared by godl url,
// torrent, webdav, and social, and builds the token-bucket limiter
// each of those download paths throttles its transfer against.
package ratelimit

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/time/rate"
)

// unit sizes match humanBytes/humanSize elsewhere in godl (cmd/format.go,
// internal/fileserver): 1024-based, not decimal, so "1M" here means the
// same thing "1.0 MiB" means when godl reports a speed back to you.
const (
	kib = 1024
	mib = kib * 1024
	gib = mib * 1024
)

// ParseRate parses a bytes-per-second limit like "500K", "2M", "1.5G", or
// a bare number of bytes/sec ("500000"). Empty string is not valid —
// callers should treat "no flag passed" as "unlimited" before calling
// this, not by passing "".
func ParseRate(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty rate")
	}

	mult := int64(1)
	switch last := s[len(s)-1]; last {
	case 'k', 'K':
		mult, s = kib, s[:len(s)-1]
	case 'm', 'M':
		mult, s = mib, s[:len(s)-1]
	case 'g', 'G':
		mult, s = gib, s[:len(s)-1]
	}

	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid rate %q: use a number optionally followed by K/M/G, e.g. 500K or 2M", s)
	}
	if n <= 0 {
		return 0, fmt.Errorf("rate must be positive, got %q", s)
	}
	return int64(n * float64(mult)), nil
}

// minBurst floors every limiter's burst at 1MiB, comfortably above the
// 256KiB read buffers used by internal/downloader and internal/webdav
// (see their copyBufSize constants) — rate.Limiter.WaitN errors
// immediately if asked to wait for more than its burst in one call, so
// the burst must never be smaller than the largest single WaitN a
// caller will ever make, even when bytesPerSec itself is small.
const minBurst = 1024 * 1024

// NewLimiter builds a token-bucket limiter capping transfers at
// bytesPerSec bytes/second, shared safely across however many
// goroutines a single job's download is split into (a *rate.Limiter is
// safe for concurrent use) so a job's concurrency doesn't multiply its
// own cap. bytesPerSec <= 0 returns nil — callers skip limiting
// entirely for a nil limiter rather than being handed a no-op one, so
// the zero value ("no flag passed") costs nothing on the hot path.
func NewLimiter(bytesPerSec int64) *rate.Limiter {
	if bytesPerSec <= 0 {
		return nil
	}
	burst := bytesPerSec
	if burst < minBurst {
		burst = minBurst
	}
	return rate.NewLimiter(rate.Limit(bytesPerSec), int(burst))
}

// WaitAll blocks until every non-nil limiter in limiters allows n more
// bytes through, so a transfer capped by more than one limiter at once
// (a job's own --limit-rate and the Settings tab's shared global cap,
// say) waits on whichever is actually more restrictive at that moment
// instead of only ever checking the first one. nil entries (an unset
// cap) and n<=0 are no-ops, so callers don't need to filter those out
// themselves before calling.
func WaitAll(ctx context.Context, n int, limiters ...*rate.Limiter) error {
	if n <= 0 {
		return nil
	}
	for _, l := range limiters {
		if l == nil {
			continue
		}
		if err := l.WaitN(ctx, n); err != nil {
			return err
		}
	}
	return nil
}
