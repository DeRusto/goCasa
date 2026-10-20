package app_test

import (
	"bufio"
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/app"
	"github.com/DeRusto/goCasa/internal/apptest"
)

// stream opens /events/stream as device h and returns its lines.
func stream(t *testing.T, h *apptest.Harness) (*http.Response, <-chan string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, h.Server.URL+"/events/stream", nil)
	res, err := h.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	lines := make(chan string)
	go func() {
		defer close(lines)
		sc := bufio.NewScanner(res.Body)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	return res, lines
}

// next is the stream's next line that starts with prefix.
func next(t *testing.T, lines <-chan string, prefix string) string {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatalf("stream ended waiting for %q", prefix)
			}
			if strings.HasPrefix(l, prefix) {
				return l
			}
		case <-timeout:
			t.Fatalf("no %q line in 5s", prefix)
		}
	}
}

func TestStreamIsUnbufferedEventStream(t *testing.T) {
	h := apptest.New(t)
	res, _ := stream(t, h)
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" || res.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("stream = %d %v", res.StatusCode, res.Header)
	}
}

func TestEditsOnOneDeviceReachAnothersStream(t *testing.T) {
	a := household(t)
	wall := a.Device()
	_, lines := stream(t, wall)

	addEvent(t, a, timed("Swim", "2026-03-16T09:00", "2026-03-16T10:00", "UTC"))
	wantKind(t, lines, "event")
	a.Post(t, "/calendar/event/1", timed("Swim club", "2026-03-16T09:00", "2026-03-16T10:00", "UTC"))
	wantKind(t, lines, "event")
	a.Post(t, "/calendar/event/1/delete", nil)
	wantKind(t, lines, "event")
	a.Post(t, "/calendar/calendars", url.Values{"name": {"School"}, "color": {"#0ea5e9"}})
	wantKind(t, lines, "event")
	a.Post(t, "/settings/members/2/archive", nil)
	wantKind(t, lines, "member")
}

// wantKind reads the stream's next change and fails unless it is of kind k:
// the SSE event name, with the kind as its data so the browser dispatches it.
func wantKind(t *testing.T, lines <-chan string, k string) {
	t.Helper()
	if l := next(t, lines, "event:"); l != "event: "+k {
		t.Fatalf("got %q, want kind %s", l, k)
	}
	if l := next(t, lines, "data:"); l != "data: "+k {
		t.Fatalf("got %q after event: %s", l, k)
	}
}
func TestQuietStreamSendsHeartbeats(t *testing.T) {
	defer app.SetHeartbeat(20 * time.Millisecond)()
	_, lines := stream(t, apptest.New(t))
	next(t, lines, ": heartbeat")
}

// ends waits for the stream to end.
func ends(t *testing.T, lines <-chan string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-lines:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("stream still open after 5s")
		}
	}
}

func TestClosingTheBusEndsStreams(t *testing.T) {
	h := apptest.New(t)
	_, lines := stream(t, h)
	next(t, lines, ": hello")
	h.App.Bus.Close()
	ends(t, lines)
	_, lines = stream(t, h)
	ends(t, lines)
}

// nothingPublished checks the stream got no change since the last one read:
// it makes one of kind event, which must be the next.
func nothingPublished(t *testing.T, h *apptest.Harness, lines <-chan string) {
	t.Helper()
	post(t, h, "/calendar/calendars", url.Values{"name": {"Marker"}, "color": {"#0ea5e9"}})
	wantKind(t, lines, "event")
}

func TestMealiePollPublishesOnlyRealChangesAndHealthTransitions(t *testing.T) {
	h, _, m := mealieHousehold(t)
	h.RunDue() // the poll a list open would make due, if any
	_, lines := stream(t, h)
	next(t, lines, ": hello")

	h.Clock.Advance(time.Minute)
	h.RunDue()
	nothingPublished(t, h, lines)

	m.addItem("L1", "I9", "Bread", false)
	h.Clock.Advance(time.Minute)
	h.RunDue()
	wantKind(t, lines, "list")

	m.setDown(true)
	for n := 1; n <= 4; n++ {
		h.Clock.Advance(time.Hour) // past any backoff
		h.RunDue()
		if n == 1 || n == 3 { // now Failing, now Down
			wantKind(t, lines, "integration")
		}
	}
	nothingPublished(t, h, lines)

	m.setDown(false)
	h.Clock.Advance(7 * time.Hour)
	h.RunDue()
	wantKind(t, lines, "integration") // recovered, with no list changed
	nothingPublished(t, h, lines)
}

func TestAFeedRefetchedUnchangedPublishesNothing(t *testing.T) {
	h := household(t)
	f := newFeed(t, vcal(swim))
	addFeed(t, h, "School", f.URL)
	_, lines := stream(t, h)
	next(t, lines, ": hello")

	f.set(strings.ReplaceAll(vcal(swim), "DTSTAMP:20260301", "DTSTAMP:20260315"), `"v2"`) // a 200, only its stamp new
	h.Clock.Advance(15 * time.Minute)
	h.RunDue()
	post(t, h, "/todo-columns", url.Values{"name": {"Marker"}}) // not an event: the next change must be this
	wantKind(t, lines, "todo")

	f.set(vcal(swim, fair), `"v3"`)
	h.Clock.Advance(15 * time.Minute)
	h.RunDue()
	wantKind(t, lines, "event")
}

func TestHouseholdSettingsPublishTheHouseholdKind(t *testing.T) {
	h := apptest.New(t)
	setup(t, h)
	_, lines := stream(t, h)
	post(t, h, "/settings/household", url.Values{"default_view": {"today"}, "week_start": {"1"}})
	wantKind(t, lines, "household")
	post(t, h, "/settings/locks", url.Values{"lock_chores": {"on"}})
	wantKind(t, lines, "household")
	post(t, h, "/settings/fallback-folder", url.Values{"enabled": {"on"}})
	wantKind(t, lines, "household")
}

func TestImmichPicksAndDisconnectPublishTheIntegrationKind(t *testing.T) {
	h, _ := immichHousehold(t)
	_, lines := stream(t, h)
	post(t, h, "/settings/immich/picks", url.Values{"album": {"alb-summer"}})
	wantKind(t, lines, "integration")
	post(t, h, "/settings/immich/delete", nil)
	wantKind(t, lines, "integration")
}
