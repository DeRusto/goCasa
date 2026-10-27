package app

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
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
// It writes the shared integrationTypes map, so tests that use it are not t.Parallel.
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
	if h := health(t, a, id); h.Failing || h.Down || h.LastError != "" || h.LastSuccess != "15 Mar 2026 09:00" {
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
	c := newIntegration{typ: "fake", name: "Fake", secret: "s", missingSecret: "Paste a key.", interval: 60, unique: true,
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

func TestConnectAddsAnotherWithItsOwnRows(t *testing.T) {
	a := household(t)
	ctx := context.Background()
	_, _, calls := fake(t, a)
	c := newIntegration{typ: "fake", name: "Fake", url: "https://u:p@x.test/feed", urlSealed: true, secret: "https://u:p@x.test/feed", interval: 60,
		then: func(tx *sql.Tx, id int64) error {
			_, err := tx.Exec("INSERT INTO source_calendars (integration_id, name, color) VALUES (?, 'Fake', '#000000')", id)
			return err
		}}
	if msg, err := a.integrations.Connect(ctx, c); msg != "" || err != nil || *calls != 1 {
		t.Fatalf("second fake: %q, %v, %d syncs", msg, err, *calls)
	}
	var n int
	a.DB.QueryRow("SELECT count(*) FROM source_calendars c JOIN integrations i ON i.id = c.integration_id WHERE i.type = 'fake'").Scan(&n)
	if n != 1 {
		t.Fatalf("%d calendars", n)
	}
	c.url = "ftp://x"
	c.badURL = "Not a feed."
	if msg, _ := a.integrations.Connect(ctx, c); msg != "Not a feed." {
		t.Fatalf("bad feed address: %q", msg)
	}
}

func TestReconnectingALoginResetsItsHealth(t *testing.T) {
	a := household(t)
	ctx := context.Background()
	id, syncErr, calls := fake(t, a)
	*syncErr = errors.New("boom")
	for range 3 {
		a.integrations.Run(ctx, id)
	}
	a.integrations.NeedsReconnect(ctx, id, nil)
	login := newIntegration{typ: "fake", name: "ada@example.com", secret: "token",
		login: &integrationLogin{find: func(*sql.Tx) (int64, string, error) { return id, "", nil }}}
	if msg, err := a.integrations.Connect(ctx, login); msg != "" || err != nil {
		t.Fatalf("reconnect: %q, %v", msg, err)
	}
	if h := health(t, a, id); h.Failing || h.Reconnect || h.LastError != "" {
		t.Fatalf("after reconnecting: %+v", h)
	}
	before := *calls
	a.syncDue(ctx, a.Now())
	if *calls != before+1 {
		t.Fatal("a reconnect isn't due straight away")
	}
	login.login.find = func(*sql.Tx) (int64, string, error) { return 0, "Wrong login.", nil }
	if msg, _ := a.integrations.Connect(ctx, login); msg != "Wrong login." {
		t.Fatalf("refused login: %q", msg)
	}
	login.login.find = func(*sql.Tx) (int64, string, error) { return 0, "", nil }
	login.login.sub = "bob"
	if msg, err := a.integrations.Connect(ctx, login); msg != "" || err != nil {
		t.Fatalf("new login: %q, %v", msg, err)
	}
	var n int
	a.DB.QueryRow("SELECT count(*) FROM integrations WHERE type = 'fake' AND account_sub = 'bob' AND next_sync_at = ?", a.Now().Unix()).Scan(&n)
	if n != 1 {
		t.Fatal("a new login isn't due straight away")
	}
}

func TestHealthsShowEveryIntegration(t *testing.T) {
	a := household(t)
	ctx := context.Background()
	id, syncErr, _ := fake(t, a)
	st, err := a.integrations.Healths(ctx)
	if err != nil || st[id].LastSuccess != "never" {
		t.Fatalf("before a sync: %+v, %v", st[id], err)
	}
	a.integrations.Run(ctx, id) // a good sync at 09:00 UTC
	if _, err := a.DB.Exec("UPDATE household_settings SET timezone = 'Europe/Paris'"); err != nil {
		t.Fatal(err)
	}
	*syncErr = errors.New("boom")
	a.integrations.Run(ctx, id)
	st, _ = a.integrations.Healths(ctx)
	if got := st[id]; got.LastSuccess != "15 Mar 2026 10:00" || got.Failures != 1 || got.LastError != "boom" || got.Reconnect {
		t.Fatalf("after a failure: %+v", got)
	}
	a.integrations.NeedsReconnect(ctx, id, nil)
	if st, _ = a.integrations.Healths(ctx); !st[id].Reconnect {
		t.Fatal("not Reconnect needed")
	}
}

// published is the kinds the bus carried since ch subscribed, once nothing more comes.
func published(ch chan web.Kind) []web.Kind {
	var ks []web.Kind
	for {
		select {
		case k := <-ch:
			ks = append(ks, k)
		case <-time.After(50 * time.Millisecond):
			return ks
		}
	}
}

func TestReconnectNeededAndAReconnectReachOpenPages(t *testing.T) {
	a := household(t)
	ctx := context.Background()
	id, _, _ := fake(t, a)
	ch := a.Bus.subscribe()
	defer a.Bus.unsubscribe(ch)
	a.integrations.NeedsReconnect(ctx, id, nil)
	if ks := published(ch); len(ks) != 1 || ks[0] != web.KindIntegration {
		t.Fatalf("becoming Reconnect needed published %v", ks)
	}
	login := newIntegration{typ: "fake", name: "ada@example.com", secret: "token",
		login: &integrationLogin{find: func(*sql.Tx) (int64, string, error) { return id, "", nil }}}
	if msg, err := a.integrations.Connect(ctx, login); msg != "" || err != nil {
		t.Fatalf("reconnect: %q, %v", msg, err)
	}
	if ks := published(ch); len(ks) != 1 || ks[0] != web.KindIntegration {
		t.Fatalf("a reconnect published %v", ks)
	}
}
