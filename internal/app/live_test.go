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
	if l := next(t, lines, "data:"); l != `data: {"type":"created","entity":"event","id":1}` {
		t.Fatalf("got %q", l)
	}
	a.Post(t, "/calendar/event/1", timed("Swim club", "2026-03-16T09:00", "2026-03-16T10:00", "UTC"))
	if l := next(t, lines, "data:"); l != `data: {"type":"updated","entity":"event","id":1}` {
		t.Fatalf("got %q", l)
	}
	a.Post(t, "/calendar/event/1/delete", nil)
	if l := next(t, lines, "data:"); l != `data: {"type":"deleted","entity":"event","id":1}` {
		t.Fatalf("got %q", l)
	}
	a.Post(t, "/calendar/calendars", url.Values{"name": {"School"}, "color": {"#0ea5e9"}})
	if l := next(t, lines, "data:"); l != `data: {"type":"created","entity":"calendar","id":2}` {
		t.Fatalf("got %q", l)
	}
	a.Post(t, "/settings/members/2/archive", nil)
	if l := next(t, lines, "data:"); l != `data: {"type":"updated","entity":"member","id":2}` {
		t.Fatalf("got %q", l)
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
