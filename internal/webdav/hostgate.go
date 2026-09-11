package webdav

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// A hostGate caps concurrent requests to one WebDAV host. It is shared
// per host across every Client in the process, because the thing that
// draws 429s is the total: each job builds its own Client, each Walk
// fans out PROPFINDs, each folder download runs its own GETs, and jobs
// start all at once by default.
//
// AIMD: halve the ceiling on a 429, widen by one after a quiet spell.

const (
	hostGateDefaultLimit = 4 // enough to keep a link busy; gentle on cloud-proxying backends
	hostGateMinLimit     = 1
)

var hostGateRecoverAfter = 5 * time.Second

type hostGate struct {
	mu       sync.Mutex
	limit    int
	inFlight int
	waiters  []chan struct{}
	// coolUntil pauses every request to this host after a 429, so one
	// rate-limit response slows the whole fleet rather than each request
	// discovering the limit for itself.
	coolUntil    time.Time
	lastThrottle time.Time
}

var (
	hostGatesMu sync.Mutex
	hostGates   = map[string]*hostGate{}
)

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

// throttled records a 429: halve the ceiling and hold every request to
// this host until pause elapses, so requests not yet sent don't walk
// into the same limit while a peer waits it out.
//
// pause is used exactly as given. "Retry-After: 0" means retry
// immediately, and substituting a default would turn the server's
// explicit go-ahead into a stall.
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

// jitter spreads a backoff by ±25%. Without it, requests throttled at
// the same moment wait the same time and collide again, turning one
// rate-limit response into a repeating cycle.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	spread := float64(d) * 0.25
	return time.Duration(float64(d) - spread + rand.Float64()*2*spread)
}
