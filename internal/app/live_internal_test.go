package app

import "testing"

func TestBusDropsASubscriberThatFallsBehind(t *testing.T) {
	b := newBus()
	slow, live := b.subscribe(), b.subscribe()
	for i := range 17 { // one past the buffer
		b.Publish(Change{Type: "created", Entity: "event", ID: int64(i)})
		<-live
	}
	for range slow {
	} // closed after its 16 buffered changes, so this ends
	b.Publish(Change{Type: "deleted", Entity: "event", ID: 1})
	if c := <-live; c.Type != "deleted" {
		t.Fatalf("live subscriber got %+v", c)
	}
	b.unsubscribe(slow) // already dropped: must not double-close
}
