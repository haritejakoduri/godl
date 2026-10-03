package webui

import (
	"bytes"
	"net/http"
	"sync"
	"time"
)

// A Broadcaster turns "what does the page need to show right now" into
// a feed for any number of open tabs at the cost of one.
//
// produce runs once per tick no matter how many tabs are subscribed,
// and not at all while none are — the job list it reads comes out of
// the same single sqlite connection the downloads checkpoint their
// progress through, so every extra query is taken from them. A payload
// identical to the last one isn't sent again.
type Broadcaster struct {
	produce  func() []byte
	interval time.Duration

	mu   sync.Mutex
	subs map[chan []byte]struct{}
	last []byte
	stop chan struct{}
}

func NewBroadcaster(interval time.Duration, produce func() []byte) *Broadcaster {
	return &Broadcaster{produce: produce, interval: interval, subs: map[chan []byte]struct{}{}}
}

// Subscribe returns a channel of payloads, starting with the current
// one, and a function that ends the subscription. The channel holds one
// payload: a subscriber that falls behind skips to the latest rather
// than queueing stale ones.
func (b *Broadcaster) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 1)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	if len(b.subs) == 1 {
		b.last = b.produce()
		b.stop = make(chan struct{})
		go b.run(b.stop)
	}
	ch <- b.last
	b.mu.Unlock()

	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subs[ch]; !ok {
			return
		}
		delete(b.subs, ch)
		if len(b.subs) == 0 {
			close(b.stop)
		}
	}
}

func (b *Broadcaster) run(stop chan struct{}) {
	t := time.NewTicker(b.interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		payload := b.produce()
		b.mu.Lock()
		if !bytes.Equal(payload, b.last) {
			b.last = payload
			for ch := range b.subs {
				select {
				case <-ch: // drop the one it hasn't read yet
				default:
				}
				ch <- payload
			}
		}
		b.mu.Unlock()
	}
}

// sseKeepAlive is how often an idle feed sends a comment line, so a
// proxy or the browser doesn't decide the connection is dead.
const sseKeepAlive = 20 * time.Second

// ServeEvents streams b to one client as Server-Sent Events until it
// disconnects. Payloads must be single-line JSON.
func ServeEvents(w http.ResponseWriter, r *http.Request, b *Broadcaster) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, cancel := b.Subscribe()
	defer cancel()
	keepAlive := time.NewTicker(sseKeepAlive)
	defer keepAlive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case payload := <-ch:
			w.Write([]byte("data: "))
			w.Write(payload)
			w.Write([]byte("\n\n"))
		case <-keepAlive.C:
			w.Write([]byte(": keep-alive\n\n"))
		}
		flusher.Flush()
	}
}
