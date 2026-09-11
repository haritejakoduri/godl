package webdav

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// The problem this file solves: nothing used to coordinate how many
// requests godl aimed at one WebDAV server at a time. Each job built its
// own Client (see internal/daemon's startWebDAV), each Walk fanned out
// walkConcurrency PROPFINDs, and each folder download ran
// webdavDownloadConcurrency GETs on top of that — so selecting several
// folders in the browser, which queues one job per selection and starts
// them all at once by default (Settings.MaxConcurrent is unlimited out of
// the box), pointed dozens of simultaneous requests at a single host.
// Rate-limiting backends answer that with 429s, and because every request
// then backed off on its own identical schedule, they all came back at
// the same moment and collided again.
//
// A gate is shared per host across every Client in the process, so the
// ceiling holds no matter how many jobs, walks and downloads are running.

// hostGateDefaultLimit is the steady-state ceiling on concurrent requests
// to one host. Deliberately modest: WebDAV backends that proxy cloud
// storage do real work per PROPFIND, and being handed four requests at a
// time is plenty to keep a link busy while staying well inside what these
// services tolerate.
const hostGateDefaultLimit = 4

// hostGateMinLimit is how far a run of 429s may shrink the ceiling. One
// in-flight request at a time is the politest godl can be while still
// making progress.
const hostGateMinLimit = 1

// hostGateRecoverAfter is how long the gate must go without a 429 before
// it widens by one. Additive increase, multiplicative decrease: back off
// fast when the server complains, return slowly once it stops.
var hostGateRecoverAfter = 5 * time.Second

type hostGate struct {
	mu sync.Mutex
	// limit is the current ceiling, between hostGateMinLimit and
	// hostGateDefaultLimit.
	limit int
	// inFlight counts requests currently holding a slot.
	inFlight int
	// waiters are signalled as slots free up.
	waiters []chan struct{}
	// coolUntil is a hard pause applied to every request to this host
	// after a 429, so one rate-limit response slows the whole fleet
	// instead of each request having to discover the limit for itself.
	coolUntil time.Time
	// lastThrottle is when the most recent 429 arrived, for the recovery
	// schedule above.
	lastThrottle time.Time
}

var (
	hostGatesMu sync.Mutex
	hostGates   = map[string]*hostGate{}
)

// gateFor returns the shared gate for host, creating it on first use.
func gateFor(host string) *hostGate {
	hostGatesMu.Lock()
	defer hostGatesMu.Unlock()
	g, ok := hostGates[host]
	if !ok {
		g = &hostGate{limit: hostGateDefaultLimit}
		hostGates[host] = g
	}
	return g
}

// acquire blocks until this host has a free slot and any cool-down from a
// recent 429 has elapsed. The returned release must be called exactly
// once; it's a no-op if acquire returned an error.
func (g *hostGate) acquire(ctx context.Context) (release func(), err error) {
	for {
		g.mu.Lock()
		// Widen back out if the server has been quiet for a while.
		if g.limit < hostGateDefaultLimit && !g.lastThrottle.IsZero() &&
			time.Since(g.lastThrottle) >= hostGateRecoverAfter {
			g.limit++
			g.lastThrottle = time.Now()
		}
		wait := time.Until(g.coolUntil)
		if wait <= 0 && g.inFlight < g.limit {
			g.inFlight++
			g.mu.Unlock()
			return g.release, nil
		}
		if wait > 0 {
			g.mu.Unlock()
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return func() {}, ctx.Err()
			}
			continue
		}
		// At the ceiling: queue up and wait for a slot to come free.
		ch := make(chan struct{})
		g.waiters = append(g.waiters, ch)
		g.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			g.cancelWaiter(ch)
			return func() {}, ctx.Err()
		}
	}
}

func (g *hostGate) release() {
	g.mu.Lock()
	g.inFlight--
	g.wakeOne()
	g.mu.Unlock()
}

// wakeOne signals one queued waiter. Caller holds g.mu.
func (g *hostGate) wakeOne() {
	if len(g.waiters) == 0 {
		return
	}
	ch := g.waiters[0]
	g.waiters = g.waiters[1:]
	close(ch)
}

// cancelWaiter removes ch from the queue after its context was canceled.
// If it was already signalled, the slot is handed to the next waiter
// rather than being lost.
func (g *hostGate) cancelWaiter(ch chan struct{}) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, w := range g.waiters {
		if w == ch {
			g.waiters = append(g.waiters[:i], g.waiters[i+1:]...)
			return
		}
	}
	// Not queued any more: it was signalled between the context firing
	// and this call, so pass the wake-up along instead of dropping it.
	g.wakeOne()
}

// throttled records a 429 from this host: halve the ceiling, and hold
// every request to it until the caller's own backoff for this response
// has elapsed, so requests that haven't been sent yet don't walk into
// the same limit while one of their peers is waiting it out.
//
// pause is passed through exactly as given — a server answering
// "Retry-After: 0" means retry essentially immediately, and substituting
// some default here would turn its explicit "go ahead" into a stall. The
// narrowed ceiling still applies either way.
func (g *hostGate) throttled(pause time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.limit /= 2
	if g.limit < hostGateMinLimit {
		g.limit = hostGateMinLimit
	}
	g.lastThrottle = time.Now()
	if pause <= 0 {
		return
	}
	if until := time.Now().Add(pause); until.After(g.coolUntil) {
		g.coolUntil = until
	}
}

// jitter spreads a backoff delay by ±25%. Without it, a burst of
// requests that were all throttled at the same moment wait exactly the
// same time and hit the server together again — turning one rate-limit
// response into a repeating collision instead of a recovery.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	spread := float64(d) * 0.25
	return time.Duration(float64(d) - spread + rand.Float64()*2*spread)
}
