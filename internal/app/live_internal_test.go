package app

import "testing"

func TestBusDropsASubscriberThatFallsBehind(t *testing.T) {
	b := newBus()
	slow, live := b.subscribe(), b.subscribe()
	for range 17 { // one past the buffer
		b.Publish(KindEvent)
		<-live
	}
	for range slow {
	} // closed after its 16 buffered changes, so this ends
	b.Publish(KindTodo)
	if c := <-live; c != KindTodo {
		t.Fatalf("live subscriber got %q", c)
	}
	b.unsubscribe(slow) // already dropped: must not double-close
}
