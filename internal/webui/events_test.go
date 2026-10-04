package webui

import (
	"sync/atomic"
	"testing"
	"time"
)

// One producer call per tick however many subscribers, none while
// nobody's subscribed, and unchanged payloads aren't resent.
func TestBroadcasterSharesOneProducer(t *testing.T) {
	var calls atomic.Int64
	var value atomic.Int64
	b := NewBroadcaster(10*time.Millisecond, func() []byte {
		calls.Add(1)
		return []byte{byte('0' + value.Load())}
	})

	time.Sleep(40 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("produce ran %d times with no subscribers, want 0", calls.Load())
	}

	ch1, cancel1 := b.Subscribe()
	ch2, cancel2 := b.Subscribe()
	if got := string(<-ch1); got != "0" {
		t.Fatalf("first payload %q, want the current one", got)
	}
	<-ch2

	before := calls.Load()
	time.Sleep(105 * time.Millisecond)
	ticks := calls.Load() - before
	if ticks < 5 || ticks > 13 {
		t.Errorf("produce ran %d times in ~10 ticks with two subscribers, want about 10 (one per tick, not per subscriber)", ticks)
	}
	select {
	case p := <-ch1:
		t.Errorf("unchanged payload %q was sent again", p)
	default:
	}

	value.Store(1)
	select {
	case p := <-ch1:
		if string(p) != "1" {
			t.Errorf("changed payload = %q, want 1", p)
		}
	case <-time.After(time.Second):
		t.Fatal("a changed payload never arrived")
	}

	cancel1()
	cancel2()
	cancel2() // twice is harmless
	time.Sleep(30 * time.Millisecond)
	after := calls.Load()
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != after {
		t.Errorf("produce kept running after the last subscriber left")
	}
}
