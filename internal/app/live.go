package app

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// heartbeat keeps reverse proxies from idle-closing a quiet stream.
var heartbeat = 25 * time.Second

// Change is what the bus carries: something of entity id was created, updated or deleted.
type Change struct {
	Type   string `json:"type"`
	Entity string `json:"entity"`
	ID     int64  `json:"id"`
}

// Bus fans every published Change out to each open /events/stream.
type Bus struct {
	mu     sync.Mutex
	subs   map[chan Change]struct{}
	closed bool
}

func newBus() *Bus { return &Bus{subs: map[chan Change]struct{}{}} }

// Publish never blocks a mutation: a stream too far behind to take a change is
// closed, and its browser reconnects and catches up by refetching.
func (b *Bus) Publish(c Change) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- c:
		default:
			delete(b.subs, ch)
			close(ch)
		}
	}
}

func (b *Bus) subscribe() chan Change {
	ch := make(chan Change, 16)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		close(ch)
	} else {
		b.subs[ch] = struct{}{}
	}
	return ch
}

func (b *Bus) unsubscribe(ch chan Change) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subs[ch]; ok {
		delete(b.subs, ch)
		close(ch)
	}
}

// Close ends every stream, so a graceful shutdown doesn't wait on them.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		for ch := range b.subs {
			delete(b.subs, ch)
			close(ch)
		}
	}
}

func (a *App) publish(typ, entity string, id int64) {
	a.Bus.Publish(Change{Type: typ, Entity: entity, ID: id})
}

func (a *App) eventStream(w http.ResponseWriter, r *http.Request) {
	ch := a.Bus.subscribe()
	defer a.Bus.unsubscribe(ch)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	w.Write([]byte(": hello\n\n"))
	if rc.Flush() != nil {
		return
	}
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		var msg []byte
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			msg = []byte(": heartbeat\n\n")
		case c, ok := <-ch:
			if !ok {
				return
			}
			b, _ := json.Marshal(c)
			msg = append(append([]byte("data: "), b...), "\n\n"...)
		}
		if _, err := w.Write(msg); err != nil || rc.Flush() != nil {
			return
		}
	}
}
