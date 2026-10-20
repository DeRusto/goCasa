package app

import (
	"context"
	"database/sql"
	"net/http"
	"slices"
	"sync"
	"time"
)

// heartbeat keeps reverse proxies from idle-closing a quiet stream.
var heartbeat = 25 * time.Second

// writeTimeout is how long one stream write may wait on a client that stopped reading.
const writeTimeout = 5 * time.Second

// Kind is what the bus carries: what sort of thing changed, so a page refetches
// only for the kinds it shows. It is the SSE event name.
type Kind string

const (
	KindEvent       Kind = "event" // events and calendars
	KindTodo        Kind = "todo"  // to-dos and their columns
	KindList        Kind = "list"  // shopping lists and items
	KindChore       Kind = "chore"
	KindPoints      Kind = "points" // balances, rewards and redemptions
	KindMember      Kind = "member"
	KindHousehold   Kind = "household"   // household settings, locks, the Fallback folder
	KindIntegration Kind = "integration" // an integration's health, settings or picks; the weather
)

// Bus fans every published Kind out to each open /events/stream.
type Bus struct {
	mu     sync.Mutex
	subs   map[chan Kind]struct{}
	closed bool
}

func newBus() *Bus { return &Bus{subs: map[chan Kind]struct{}{}} }

// Publish never blocks a mutation: a stream too far behind to take a change is
// closed, and its browser reconnects and catches up by refetching.
func (b *Bus) Publish(c Kind) {
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

func (b *Bus) subscribe() chan Kind {
	ch := make(chan Kind, 16)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		close(ch)
	} else {
		b.subs[ch] = struct{}{}
	}
	return ch
}

func (b *Bus) unsubscribe(ch chan Kind) {
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

// commit commits tx, then publishes each of kinds once: the only way a change
// reaches open pages, so none sees it before it is committed.
func (a *App) commit(tx *sql.Tx, kinds ...Kind) error {
	if err := tx.Commit(); err != nil {
		return err
	}
	for i, k := range kinds {
		if k != "" && !slices.Contains(kinds[:i], k) {
			a.Bus.Publish(k)
		}
	}
	return nil
}

// inTx runs fn in a transaction, committing if it succeeds and then publishing kinds.
func (a *App) inTx(ctx context.Context, fn func(*sql.Tx) error, kinds ...Kind) error {
	return a.inTxIf(ctx, nil, fn, kinds...)
}

// inTxIf is inTx publishing kinds only if fn set *changed: a sync that found
// nothing new publishes nothing.
func (a *App) inTxIf(ctx context.Context, changed *bool, fn func(*sql.Tx) error, kinds ...Kind) error {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	if changed != nil && !*changed {
		kinds = nil
	}
	return a.commit(tx, kinds...)
}

// exec runs one statement and, once committed, publishes k if it changed a row.
func (a *App) exec(ctx context.Context, k Kind, query string, args ...any) (sql.Result, error) {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		k = ""
	}
	return res, a.commit(tx, k)
}

// execOne runs query in tx. No row changed is sql.ErrNoRows, so inTx
// publishes nothing.
func execOne(ctx context.Context, tx *sql.Tx, query string, args ...any) error {
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (a *App) eventStream(w http.ResponseWriter, r *http.Request) {
	ch := a.Bus.subscribe()
	defer a.Bus.unsubscribe(ch)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	// send gives up on a client that stopped reading, so its handler can't block shutdown.
	send := func(msg []byte) bool {
		rc.SetWriteDeadline(time.Now().Add(writeTimeout))
		_, err := w.Write(msg)
		return err == nil && rc.Flush() == nil
	}
	if !send([]byte(": hello\n\n")) {
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
			// The kind is its data too: the browser drops an event with none.
			msg = []byte("event: " + c + "\ndata: " + c + "\n\n")
		}
		if !send(msg) {
			return
		}
	}
}
