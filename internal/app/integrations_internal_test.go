package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeSync is an Integration whose next Sync returns err (after started is
// signalled and release closed, when they are set).
type fakeSync struct {
	err     *error
	calls   *int
	started chan struct{}
	release chan struct{}
}

func (f fakeSync) Sync(context.Context, *App, integrationRow) error {
	*f.calls++
	if f.started != nil {
		f.started <- struct{}{}
		<-f.release
	}
	return *f.err
}

// fake adds an integration of a fake type to a, due now, whose syncs return *err.
func fake(t *testing.T, a *App) (id int64, err *error, calls *int) {
	t.Helper()
	err, calls = new(error), new(int)
	integrationTypes["fake"] = fakeSync{err: err, calls: calls}
	t.Cleanup(func() { delete(integrationTypes, "fake") })
	if qerr := a.DB.QueryRow("INSERT INTO integrations (type, name, sync_interval_seconds, next_sync_at) VALUES ('fake', 'Fake', 600, 0) RETURNING id").Scan(&id); qerr != nil {
		t.Fatal(qerr)
	}
	return id, err, calls
}

func health(t *testing.T, a *App, id int64) integrationHealth {
	t.Helper()
	h, err := a.integrations.Health(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestOneFailureIsFailingAndThreeAreDown(t *testing.T) {
	a := household(t)
	ctx := context.Background()
	id, syncErr, _ := fake(t, a)
	*syncErr = errors.New("boom")
	for i, want := range []integrationHealth{{Failing: true, Failures: 1}, {Failing: true, Failures: 2}, {Failing: true, Down: true, Failures: 3}} {
		synced, err := a.integrations.Run(ctx, id)
		if err != nil || synced {
			t.Fatalf("run %d: synced %v, err %v", i+1, synced, err)
		}
		h := health(t, a, id)
		if h.Failing != want.Failing || h.Down != want.Down || h.Failures != want.Failures || h.LastError != "boom" {
			t.Fatalf("after %d failures: %+v", i+1, h)
		}
	}
	*syncErr = nil
	if synced, err := a.integrations.Run(ctx, id); err != nil || !synced {
		t.Fatalf("good run: synced %v, err %v", synced, err)
	}
	if h := health(t, a, id); h.Failing || h.Down || h.LastError != "" || h.LastSuccess.Int64 != a.Now().Unix() {
		t.Fatalf("after a good sync: %+v", h)
	}
}

func TestFailuresBackOffDoublingUntilTransient(t *testing.T) {
	a := household(t)
	ctx := context.Background()
	start := a.Now()
	_, syncErr, calls := fake(t, a)
	*syncErr = errors.New("boom")
	now := start
	for _, wait := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
		a.Now = func() time.Time { return now }
		before := *calls
		if err := a.syncDue(ctx, now); err != nil || *calls != before+1 {
			t.Fatalf("at %v: calls %d, err %v", now.Sub(start), *calls, err)
		}
		if err := a.syncDue(ctx, now.Add(wait-time.Second)); err != nil || *calls != before+1 {
			t.Fatalf("retried before %v", wait)
		}
		now = now.Add(wait)
	}
	// A transient failure waits the normal interval (600s), not the backoff.
	*syncErr = transientError{errors.New("timeout")}
	a.Now = func() time.Time { return now }
	if err := a.syncDue(ctx, now); err != nil {
		t.Fatal(err)
	}
	before := *calls
	a.syncDue(ctx, now.Add(599*time.Second))
	if *calls != before {
		t.Fatal("transient failure retried before its interval")
	}
	a.syncDue(ctx, now.Add(600*time.Second))
	if *calls != before+1 {
		t.Fatal("transient failure not retried at its interval")
	}
}

func TestFailureMessageIsCutTo500Runes(t *testing.T) {
	a := household(t)
	id, syncErr, _ := fake(t, a)
	*syncErr = errors.New(strings.Repeat("é", 600))
	a.integrations.Run(context.Background(), id)
	if got := health(t, a, id).LastError; got != strings.Repeat("é", 500) {
		t.Fatalf("last error is %d runes", len([]rune(got)))
	}
	if err := a.integrations.WithLock(id, func() error {
		return a.integrations.RecordFailure(context.Background(), id, errors.New(strings.Repeat("x", 501)))
	}); err != nil {
		t.Fatal(err)
	}
	if h := health(t, a, id); h.LastError != strings.Repeat("x", 500) {
		t.Fatalf("recorded failure is %d runes", len([]rune(h.LastError)))
	}
}

func TestARecordedWriteFailureIsDown(t *testing.T) {
	a := household(t)
	id, _, _ := fake(t, a)
	if err := a.integrations.RecordFailure(context.Background(), id, errors.New("refused")); err != nil {
		t.Fatal(err)
	}
	if h := health(t, a, id); !h.Down || h.LastError != "refused" {
		t.Fatalf("after a write failure: %+v", h)
	}
}

func TestReconnectNeededWaitsForAReconnect(t *testing.T) {
	a := household(t)
	ctx := context.Background()
	id, _, calls := fake(t, a)
	if err := a.integrations.NeedsReconnect(ctx, id, nil); err != nil {
		t.Fatal(err)
	}
	if !health(t, a, id).Reconnect {
		t.Fatal("not Reconnect needed")
	}
	a.syncDue(ctx, a.Now())
	if *calls != 0 {
		t.Fatal("synced while Reconnect needed")
	}
	if err := a.inTx(ctx, func(tx *sql.Tx) error { return a.integrations.Reconnected(ctx, tx, id) }); err != nil {
		t.Fatal(err)
	}
	if health(t, a, id).Reconnect {
		t.Fatal("still Reconnect needed after a reconnect")
	}
	a.syncDue(ctx, a.Now())
	if *calls != 1 {
		t.Fatal("reconnect didn't sync straight away")
	}
}

func TestAWriteWaitsForASyncInProgress(t *testing.T) {
	a := household(t)
	id, syncErr, calls := fake(t, a)
	started, release := make(chan struct{}), make(chan struct{})
	integrationTypes["fake"] = fakeSync{err: syncErr, calls: calls, started: started, release: release}
	*syncErr = errors.New("down")
	go a.integrations.Run(context.Background(), id)
	<-started
	wrote := make(chan bool)
	go a.integrations.WithLock(id, func() error {
		wrote <- true
		return nil
	})
	select {
	case <-wrote:
		t.Fatal("write ran during the sync")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-wrote
	if !health(t, a, id).Failing {
		t.Fatal("the write ran before the sync's failure was recorded")
	}
}

func TestConnectChecksTheAddress(t *testing.T) {
	a := household(t)
	ctx := context.Background()
	probed := false
	c := connection{typ: "fake", name: "Fake", secret: "s", missingSecret: "Paste a key.", interval: 60,
		check: func(context.Context) string { probed = true; return "" }}
	for raw, want := range map[string]string{
		"ftp://x":            "Fake's address starts with https:// or http://.",
		"https://u:p@x.test": "Leave the user name and password out of Fake's address.",
	} {
		c.url = raw
		if msg, err := a.integrations.Connect(ctx, c); err != nil || msg != want {
			t.Fatalf("%s: %q, %v", raw, msg, err)
		}
	}
	c.url, c.secret = "https://x.test", ""
	if msg, _ := a.integrations.Connect(ctx, c); msg != "Paste a key." || probed {
		t.Fatalf("empty secret: %q, probed %v", msg, probed)
	}
	_, _, calls := fake(t, a) // also adds one Fake already
	c.secret = "s"
	if msg, _ := a.integrations.Connect(ctx, c); msg != "Fake is already connected. Disconnect it first." || probed {
		t.Fatalf("second connect: %q, probed %v", msg, probed)
	}
	a.DB.Exec("DELETE FROM integrations WHERE type = 'fake'")
	c.check = func(context.Context) string { probed = true; return "No." }
	if msg, _ := a.integrations.Connect(ctx, c); msg != "No." {
		t.Fatalf("refused probe: %q", msg)
	}
	c.check = func(context.Context) string { return "" }
	if msg, err := a.integrations.Connect(ctx, c); msg != "" || err != nil || *calls != 1 {
		t.Fatalf("connect: %q, %v, %d syncs", msg, err, *calls)
	}
}
