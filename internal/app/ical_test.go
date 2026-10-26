package app_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/app"
	"github.com/DeRusto/goCasa/internal/apptest"
)

// fakeFeed serves an iCal body with an ETag, answering 304 to a matching If-None-Match.
type fakeFeed struct {
	mu          sync.Mutex
	body, etag  string
	status      int      // when set, answered instead of the feed
	conditional []string // the If-None-Match of each request
	*httptest.Server
}

func newFeed(t *testing.T, body string) *fakeFeed {
	f := &fakeFeed{body: body, etag: `"v1"`}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.conditional = append(f.conditional, r.Header.Get("If-None-Match"))
		switch {
		case f.status != 0:
			w.WriteHeader(f.status)
		case r.Header.Get("If-None-Match") == f.etag:
			w.WriteHeader(http.StatusNotModified)
		default:
			w.Header().Set("ETag", f.etag)
			w.Write([]byte(f.body))
		}
	}))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeFeed) set(body, etag string) { f.mu.Lock(); f.body, f.etag = body, etag; f.mu.Unlock() }
func (f *fakeFeed) fail(status int)       { f.mu.Lock(); f.status = status; f.mu.Unlock() }
func (f *fakeFeed) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.conditional...)
}

func vcal(events ...string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\n" + strings.Join(events, "") + "END:VCALENDAR\r\n"
}

func vevent(lines ...string) string {
	return "BEGIN:VEVENT\r\nDTSTAMP:20260301T000000Z\r\n" + strings.Join(lines, "\r\n") + "\r\nEND:VEVENT\r\n"
}

var (
	swim   = vevent("UID:swim@school", "SUMMARY:Swim meet", "DTSTART:20260316T170000Z", "DTEND:20260316T180000Z")
	fair   = vevent("UID:fair@school", "SUMMARY:Book fair", "DTSTART;VALUE=DATE:20260317", "DTEND;VALUE=DATE:20260319")
	recess = vevent("UID:recess@school", "SUMMARY:Recess", "DTSTART;TZID=America/Los_Angeles:20260316T100000", "DTEND;TZID=America/Los_Angeles:20260316T103000",
		"RRULE:FREQ=DAILY;COUNT=4", "EXDATE;TZID=America/Los_Angeles:20260317T100000")
	recessMoved = vevent("UID:recess@school", "RECURRENCE-ID;TZID=America/Los_Angeles:20260318T100000", "SUMMARY:Recess (late)",
		"DTSTART;TZID=America/Los_Angeles:20260318T110000", "DTEND;TZID=America/Los_Angeles:20260318T113000")
	extra = vevent("UID:extra@school", "SUMMARY:Extra practice", "DTSTART:20260316T200000Z", "DTEND:20260316T210000Z", "RDATE:20260320T200000Z")
	// Seconds on a UTC Series: its overrides still match their Occurrences.
	choir     = vevent("UID:choir@school", "SUMMARY:Choir", "DTSTART:20260316T120030Z", "DTEND:20260316T130030Z", "RRULE:FREQ=DAILY;COUNT=3")
	choirOff  = vevent("UID:choir@school", "RECURRENCE-ID:20260317T120030Z", "STATUS:CANCELLED", "DTSTART:20260317T120030Z", "DTEND:20260317T130030Z")
	recessOff = vevent("UID:recess@school", "RECURRENCE-ID;TZID=America/Los_Angeles:20260319T100000", "STATUS:CANCELLED", "SUMMARY:Recess",
		"DTSTART;TZID=America/Los_Angeles:20260319T100000", "DTEND;TZID=America/Los_Angeles:20260319T103000")
)

// addFeed adds a feed to household h (calendar 1 is local) as its calendar 2.
func addFeed(t *testing.T, h *apptest.Harness, name, feedURL string) {
	t.Helper()
	if code, body := h.Post(t, "/settings/feeds", url.Values{"name": {name}, "url": {feedURL}, "color": {"#f59e0b"}}); code != http.StatusOK {
		t.Fatalf("add feed = %d %q", code, body)
	}
}

func agendaPage(t *testing.T, h *apptest.Harness) string {
	t.Helper()
	_, page := h.Get(t, "/calendar?view=agenda&date=2026-03-15")
	return page
}

// settings unlocks h again, since syncing waits longer than an unlock lasts, and shows Settings.
func settings(t *testing.T, h *apptest.Harness) string {
	t.Helper()
	unlock(t, h, 1, "1234")
	_, page := h.Get(t, "/settings")
	return page
}

func TestFeedImportUpdateAndSoftDelete(t *testing.T) {
	h := household(t)
	f := newFeed(t, vcal(swim, fair))
	addFeed(t, h, "School", f.URL)
	page := agendaPage(t, h)
	if !strings.Contains(page, `title="From a calendar feed: read-only"`) {
		t.Fatalf("feed events aren't marked read-only:\n%s", page)
	}
	for _, want := range []string{"17:00–18:00 Swim meet", "All day Book fair"} {
		if !strings.Contains(text(page), want) {
			t.Fatalf("calendar lacks %q:\n%s", want, page)
		}
	}
	if d := day(t, page, "2026-03-18"); !strings.Contains(d, "Book fair") {
		t.Fatalf("the fair's last day = %q", d)
	}
	if !strings.Contains(settings(t, h), "Last synced: 15 Mar 2026 09:00") {
		t.Fatal("Settings doesn't show the sync")
	}
	post(t, h, "/settings/household", url.Values{"default_view": {"today"}, "week_start": {"0"}, "clock_24h": {"on"}, "timezone": {"Asia/Kolkata"}})
	if !strings.Contains(settings(t, h), "Last synced: 15 Mar 2026 14:30") {
		t.Fatal("Settings doesn't show the sync in the household timezone")
	}
	post(t, h, "/settings/household", url.Values{"default_view": {"today"}, "week_start": {"0"}, "clock_24h": {"on"}, "timezone": {""}})

	// Unchanged: a 304, after the sync interval.
	h.Clock.Advance(14 * time.Minute)
	h.RunDue()
	if n := len(f.requests()); n != 1 {
		t.Fatalf("synced again before its interval: %d requests", n)
	}
	h.Clock.Advance(time.Minute)
	h.RunDue()
	if got := f.requests(); len(got) != 2 || got[1] != `"v1"` {
		t.Fatalf("If-None-Match sent = %q", got)
	}

	// The swim meet moves and is renamed; the fair leaves the feed.
	f.set(vcal(strings.ReplaceAll(strings.ReplaceAll(swim, "Swim meet", "Swim gala"), "T170000Z", "T160000Z")), `"v2"`)
	h.Clock.Advance(15 * time.Minute)
	h.RunDue()
	page = agendaPage(t, h)
	if !strings.Contains(text(page), "16:00–18:00 Swim gala") || strings.Contains(page, "Swim meet") || strings.Contains(page, "Book fair") {
		t.Fatalf("after the upstream edit and delete:\n%s", page)
	}
	var rows int
	h.App.DB.QueryRow("SELECT count(*) FROM events WHERE external_id = 'fair@school' AND status = 'cancelled'").Scan(&rows)
	if rows != 1 {
		t.Fatal("the fair wasn't soft-deleted")
	}

	// Back in the feed, it shows again.
	f.set(vcal(swim, fair), `"v3"`)
	h.Clock.Advance(15 * time.Minute)
	h.RunDue()
	if !strings.Contains(agendaPage(t, h), "Book fair") {
		t.Fatal("the fair didn't come back")
	}

	// A copy it can't read keeps the last good one.
	f.set(vcal(swim, strings.ReplaceAll(fair, "DTSTART;VALUE=DATE:20260317", "DTSTART;VALUE=DATE:2026-03-17")), `"v4"`)
	h.Clock.Advance(15 * time.Minute)
	h.RunDue()
	if !strings.Contains(agendaPage(t, h), "Book fair") {
		t.Fatal("an unreadable copy deleted the fair")
	}
}

func TestFeedSeries(t *testing.T) {
	h := household(t)
	addFeed(t, h, "School", newFeed(t, vcal(recessOff, recess, recessMoved, extra, choirOff, choir)).URL) // overrides before their Series
	page := agendaPage(t, h)
	for date, want := range map[string]string{"2026-03-16": "17:00–17:30 Recess", "2026-03-18": "18:00–18:30 Recess (late)",
		"2026-03-20": "20:00–21:00 Extra practice"} {
		if d := day(t, page, date); !strings.Contains(text(d), want) {
			t.Fatalf("%s = %q, want %q", date, d, want)
		}
	}
	// The EXDATE, the cancelled Recess and the cancelled Choir leave nothing on these days.
	for _, date := range []string{"2026-03-17", "2026-03-19"} {
		if strings.Contains(page, `data-date="`+date+`"`) {
			t.Fatalf("%s shows:\n%s", date, page)
		}
	}
	if d := day(t, page, "2026-03-18"); !strings.Contains(d, "Choir") {
		t.Fatalf("2026-03-18 = %q", d)
	}
}

func TestFeedEventsAreReadOnly(t *testing.T) {
	h := household(t)
	addFeed(t, h, "School", newFeed(t, vcal(swim)).URL)
	var id string
	h.App.DB.QueryRow("SELECT id FROM events WHERE external_id = 'swim@school'").Scan(&id)
	if code, _ := h.Get(t, "/calendar/event/"+id); code != http.StatusNotFound {
		t.Fatalf("open = %d", code)
	}
	if code, _ := h.Post(t, "/calendar/event/"+id, timed("Mine now", "2026-03-16T09:00", "2026-03-16T10:00", "UTC")); code != http.StatusNotFound {
		t.Fatalf("edit = %d", code)
	}
	if code, _ := h.Post(t, "/calendar/event/"+id+"/delete", nil); code != http.StatusNotFound {
		t.Fatalf("delete = %d", code)
	}
	f := timed("Sneaky", "2026-03-16T09:00", "2026-03-16T10:00", "UTC")
	f.Set("calendar_id", "2")
	if code, _ := h.Post(t, "/calendar/event", f); code != http.StatusBadRequest {
		t.Fatalf("create on the feed's calendar = %d", code)
	}
	if page := agendaPage(t, h); !strings.Contains(page, "Swim meet") || strings.Contains(page, "Mine now") || strings.Contains(page, "Sneaky") {
		t.Fatalf("calendar:\n%s", page)
	}
}

func TestBrokenFeedBacksOffAndLeavesOthersAlone(t *testing.T) {
	h := household(t)
	bad, good := newFeed(t, vcal(swim)), newFeed(t, vcal(fair))
	addFeed(t, h, "Club", bad.URL)
	addFeed(t, h, "School", good.URL)

	bad.set("<html>not a calendar</html>", `"v2"`)
	good.set(vcal(fair, strings.ReplaceAll(strings.ReplaceAll(swim, "swim@", "play@"), "Swim meet", "School play")), `"v2"`)
	h.Clock.Advance(15 * time.Minute)
	h.RunDue()
	page := agendaPage(t, h)
	if !strings.Contains(page, "Swim meet") || !strings.Contains(page, "School play") {
		t.Fatalf("a garbage feed changed its events, or stopped another's sync:\n%s", page)
	}
	if s := settings(t, h); !strings.Contains(s, "Sync failing (1 in a row): that isn&#39;t an iCal feed") {
		t.Fatalf("Settings doesn't show the error:\n%s", s)
	}

	// Backoff: 1 minute, then 2.
	h.Clock.Advance(time.Minute)
	h.RunDue()
	h.Clock.Advance(time.Minute)
	h.RunDue()
	if n := len(bad.requests()); n != 3 {
		t.Fatalf("requests = %d, want 3", n)
	}
	bad.fail(http.StatusInternalServerError)
	h.Clock.Advance(time.Minute)
	h.RunDue()
	if s := settings(t, h); !strings.Contains(s, "Sync failing (3 in a row): the feed answered 500 Internal Server Error") {
		t.Fatalf("Settings:\n%s", s)
	}

	bad.fail(0)
	bad.set(vcal(swim), `"v3"`)
	h.Clock.Advance(15 * time.Minute) // a 500 waits the feed's interval, not the backoff
	h.RunDue()
	if s := settings(t, h); strings.Contains(s, "Sync failing") {
		t.Fatalf("still failing after a good sync:\n%s", s)
	}
}

func TestFeedHostAnswering503DoesNotBackOff(t *testing.T) {
	h := household(t)
	f := newFeed(t, vcal(swim))
	addFeed(t, h, "School", f.URL)
	f.fail(http.StatusServiceUnavailable)
	h.Clock.Advance(15 * time.Minute)
	h.RunDue()
	n := len(f.requests())
	h.Clock.Advance(time.Minute)
	h.RunDue()
	if got := len(f.requests()); got != n {
		t.Fatal("a 503 backed off (retried after a minute) instead of waiting the feed's interval")
	}
	h.Clock.Advance(14 * time.Minute)
	h.RunDue()
	if got := len(f.requests()); got != n+1 {
		t.Fatalf("requests = %d, want a retry at the 15-minute interval", got-n)
	}
}

func TestSyncInterruptedMidTransaction(t *testing.T) {
	h := household(t)
	f := newFeed(t, vcal(swim, fair))
	addFeed(t, h, "School", f.URL)

	f.set(vcal(strings.ReplaceAll(swim, "Swim meet", "Swim gala"), strings.ReplaceAll(fair, "Book fair", "Book sale")), `"v2"`)
	defer app.SetSyncFault(func(n int) {
		if n == 2 {
			panic("power cut")
		}
	})()
	h.Clock.Advance(15 * time.Minute)
	h.RunDue()
	page := agendaPage(t, h)
	if !strings.Contains(page, "Swim meet") || !strings.Contains(page, "Book fair") || strings.Contains(page, "gala") {
		t.Fatalf("a half-done sync was kept:\n%s", page)
	}
	if s := settings(t, h); !strings.Contains(s, "crashed: power cut") {
		t.Fatalf("Settings:\n%s", s)
	}

	app.SetSyncFault(func(int) {})
	h.Clock.Advance(time.Minute)
	h.RunDue()
	if got := f.requests(); got[len(got)-1] != `"v1"` {
		t.Fatalf("the failed sync saved its ETag: %q", got)
	}
	if page := agendaPage(t, h); !strings.Contains(page, "Swim gala") || !strings.Contains(page, "Book sale") {
		t.Fatalf("the retry didn't apply:\n%s", page)
	}
}

func TestFeedURLIsEncrypted(t *testing.T) {
	h := household(t)
	f := newFeed(t, vcal(swim))
	addFeed(t, h, "School", f.URL+"/private/s3cret-token.ics")
	addFeed(t, h, "Gone", "webcal://127.0.0.1:1/also-s3cret-token.ics") // fails: its error mustn't repeat the URL
	if !strings.Contains(agendaPage(t, h), "Swim meet") {
		t.Fatal("the feed didn't sync")
	}
	if s := settings(t, h); !strings.Contains(s, "Sync failing") || strings.Contains(s, "s3cret") {
		t.Fatalf("Settings:\n%s", s)
	}
	files, _ := filepath.Glob(filepath.Join(h.App.Config.DataDir, "app.db*"))
	for _, file := range files {
		b, _ := os.ReadFile(file)
		if strings.Contains(string(b), "s3cret") {
			t.Fatalf("%s holds the feed URL in plaintext", file)
		}
	}
}

func TestRemoveFeed(t *testing.T) {
	h := household(t)
	addFeed(t, h, "School", newFeed(t, vcal(swim)).URL)
	var id int64
	h.App.DB.QueryRow("SELECT id FROM integrations WHERE type = 'ical'").Scan(&id)
	if code, _ := h.Post(t, "/settings/feeds/"+strconv.FormatInt(id, 10)+"/delete", nil); code != http.StatusOK {
		t.Fatalf("remove = %d", code)
	}
	if page := agendaPage(t, h); strings.Contains(page, "Swim meet") {
		t.Fatal("its events stayed")
	}
	if code, _ := h.Post(t, "/settings/feeds/1/delete", nil); code != http.StatusOK {
		t.Fatal("removing the local integration should be a no-op")
	}
	var n int
	h.App.DB.QueryRow("SELECT count(*) FROM integrations WHERE type = 'local'").Scan(&n)
	if n != 1 {
		t.Fatal("the local integration was deleted")
	}
}
