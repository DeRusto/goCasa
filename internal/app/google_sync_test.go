package app_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/apptest"
)

func gTimed(id, summary, start, end string) map[string]any {
	return map[string]any{"id": id, "status": "confirmed", "summary": summary, "start": map[string]any{"dateTime": start}, "end": map[string]any{"dateTime": end}}
}

func gAllDay(id, summary, first, end string) map[string]any {
	return map[string]any{"id": id, "status": "confirmed", "summary": summary, "start": map[string]any{"date": first}, "end": map[string]any{"date": end}}
}

func gCancelled(id string) map[string]any { return map[string]any{"id": id, "status": "cancelled"} }

// gOccurrence is an occurrence of Google series series.
func gOccurrence(series, id, summary, start, end string) map[string]any {
	e := gTimed(id, summary, start, end)
	e["recurringEventId"] = series
	return e
}

// syncedGoogle connects Alice's Google account with her calendar shown and
// Bob (Member 2) as its default Member. It returns the calendar's id.
func syncedGoogle(t *testing.T, g *fakeGoogle, opts ...apptest.Option) (*apptest.Harness, int64) {
	t.Helper()
	g.list(cal("alice@example.com", "Alice", "#9fc6e7", "owner"))
	h := googleHousehold(t, g, opts...)
	h.Post(t, "/integrations/google/connect", nil)
	var id int64
	h.App.DB.QueryRow("SELECT id FROM source_calendars WHERE external_id = 'alice@example.com'").Scan(&id)
	if code, _ := h.Post(t, fmt.Sprintf("/settings/google-calendars/%d", id), url.Values{"enabled": {"on"}, "member": {"2"}}); code != http.StatusOK {
		t.Fatalf("showing the calendar = %d", code)
	}
	return h, id
}

func (g *fakeGoogle) set(f func(g *fakeGoogle)) { g.mu.Lock(); defer g.mu.Unlock(); f(g) }
func (g *fakeGoogle) requests() []url.Values {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]url.Values(nil), g.lists...)
}

func stored(t *testing.T, h *apptest.Harness, cal int64) string {
	t.Helper()
	var ids string
	h.App.DB.QueryRow("SELECT coalesce(group_concat(external_id, ',' ORDER BY external_id), '') FROM events WHERE calendar_id = ?", cal).Scan(&ids)
	return ids
}

func eventID(t *testing.T, h *apptest.Harness, external string) int64 {
	t.Helper()
	var id int64
	if err := h.App.DB.QueryRow("SELECT id FROM events WHERE external_id = ?", external).Scan(&id); err != nil {
		t.Fatalf("event %s: %v", external, err)
	}
	return id
}

func TestGoogleSyncKeepsTheWindowInStep(t *testing.T) {
	g := newGoogle(t)
	dentist := gTimed("dentist", "Dentist", "2026-03-16T10:00:00Z", "2026-03-16T11:00:00Z")
	dentist["start"].(map[string]any)["timeZone"] = "Europe/London"
	g.events["alice@example.com"] = []map[string]any{
		dentist,
		gAllDay("camp", "Camp", "2026-03-17", "2026-03-19"),
		// The window is 3 months back to 12 months ahead of 2026-03-15 09:00.
		gTimed("ended", "Ended as the window starts", "2025-12-15T08:00:00Z", "2025-12-15T09:00:00Z"),
		gTimed("lastin", "Ends just inside", "2025-12-15T08:00:00Z", "2025-12-15T09:01:00Z"),
		gTimed("ahead", "Starts just inside", "2027-03-15T08:59:00Z", "2027-03-15T10:00:00Z"),
		gTimed("toofar", "Starts as the window ends", "2027-03-15T09:00:00Z", "2027-03-15T10:00:00Z"),
		gOccurrence("swim", "swim_1", "Swim", "2026-03-21T10:00:00Z", "2026-03-21T11:00:00Z"),
		gOccurrence("swim", "swim_2", "Swim", "2026-03-28T10:00:00Z", "2026-03-28T11:00:00Z"),
		gOccurrence("yoga", "yoga_1", "Yoga", "2026-03-22T10:00:00Z", "2026-03-22T11:00:00Z"),
		gOccurrence("yoga", "yoga_2", "Yoga", "2026-03-29T10:00:00Z", "2026-03-29T11:00:00Z"),
	}
	h, alice := syncedGoogle(t, g)
	h.RunDue()

	// First import: every page, windowed by Google and again by GoCasa.
	if got := stored(t, h, alice); got != "ahead,camp,dentist,lastin,swim_1,swim_2,yoga_1,yoga_2" {
		t.Fatalf("first import stored %q", got)
	}
	reqs := g.requests()
	if len(reqs) != 5 {
		t.Fatalf("%d list requests for 5 pages", len(reqs))
	}
	if q := reqs[0]; q.Get("singleEvents") != "true" || q.Get("timeMin") != "2025-12-15T09:00:00Z" || q.Get("timeMax") != "2027-03-15T09:00:00Z" || q.Has("syncToken") {
		t.Fatalf("full import asked %v", q)
	}
	page := agendaPage(t, h)
	if d := day(t, page, "2026-03-16"); !strings.Contains(text(d), "10:00–11:00 Dentist") || !strings.Contains(d, "#22c55e") {
		t.Fatalf("the dentist (Bob's by default) = %q", d)
	}
	if d := day(t, page, "2026-03-18"); !strings.Contains(text(d), "All day Camp") {
		t.Fatalf("camp's last day = %q", d)
	}
	var tzs string
	h.App.DB.QueryRow("SELECT group_concat(start_tzid, ',' ORDER BY external_id) FROM events WHERE external_id IN ('dentist', 'lastin')").Scan(&tzs)
	if tzs != "Europe/London,America/New_York" { // an event's own zone, else its calendar's
		t.Fatalf("zones = %q", tzs)
	}

	// A Member change made in GoCasa.
	dentistID := eventID(t, h, "dentist")
	if code, page := h.Get(t, fmt.Sprintf("/calendar/synced/%d", dentistID)); code != http.StatusOK || !strings.Contains(page, "Dentist") {
		t.Fatalf("synced event page = %d:\n%s", code, page)
	}
	if code, _ := h.Post(t, fmt.Sprintf("/calendar/synced/%d", dentistID), url.Values{"members": {"1"}}); code != http.StatusOK {
		t.Fatalf("changing its Members = %d", code)
	}

	// Incremental: an edit, a cancelled occurrence, a whole series deleted by its
	// own id, one moved out of the window; and the window slides past lastin.
	g.set(func(g *fakeGoogle) {
		g.changes["alice@example.com"] = []map[string]any{
			gTimed("dentist", "Dentist (moved)", "2026-03-16T14:00:00Z", "2026-03-16T15:00:00Z"),
			gCancelled("camp"),
			gCancelled("swim_1"),
			gCancelled("yoga"),
			gTimed("ahead", "Moved past the window", "2028-01-01T10:00:00Z", "2028-01-01T11:00:00Z"),
			gTimed("new", "New", "2026-03-20T10:00:00Z", "2026-03-20T11:00:00Z"),
		}
	})
	h.Clock.Advance(2 * time.Minute)
	h.RunDue()
	reqs = g.requests()
	if q := reqs[len(reqs)-1]; q.Get("syncToken") != "token-5" || q.Get("singleEvents") != "true" || q.Has("timeMin") {
		t.Fatalf("incremental sync asked %v", q)
	}
	if got := stored(t, h, alice); got != "dentist,new,swim_2" {
		t.Fatalf("after the incremental sync %q", got)
	}
	page = agendaPage(t, h)
	if d := day(t, page, "2026-03-16"); !strings.Contains(text(d), "14:00–15:00 Dentist (moved)") || strings.Contains(d, "#22c55e") {
		t.Fatalf("the moved dentist lost its Member change: %q", d)
	}
	if d := day(t, page, "2026-03-20"); !strings.Contains(d, "#22c55e") {
		t.Fatalf("a new event lacks its calendar's default Member: %q", d)
	}

	// New defaults reach the events without a GoCasa Member change.
	settings(t, h)
	h.Post(t, fmt.Sprintf("/settings/google-calendars/%d", alice), url.Values{"enabled": {"on"}})
	if d := day(t, agendaPage(t, h), "2026-03-20"); strings.Contains(d, "#22c55e") {
		t.Fatalf("an event kept its calendar's old default: %q", d)
	}
	if d := day(t, agendaPage(t, h), "2026-03-16"); !strings.Contains(d, "#3b82f6") {
		t.Fatalf("new defaults overwrote a Member change: %q", d)
	}

	// Hidden calendars aren't shown or synced.
	h.Post(t, fmt.Sprintf("/settings/google-calendars/%d", alice), url.Values{"member": {"2"}})
	if page := agendaPage(t, h); strings.Contains(page, "Dentist") {
		t.Fatal("a hidden calendar's events still show")
	}
	n := len(g.requests())
	h.Clock.Advance(2 * time.Minute)
	h.RunDue()
	if len(g.requests()) != n {
		t.Fatal("a hidden calendar was synced")
	}
}

func TestGoogleFullReimports(t *testing.T) {
	g := newGoogle(t)
	g.events["alice@example.com"] = []map[string]any{gTimed("dentist", "Dentist", "2026-03-16T10:00:00Z", "2026-03-16T11:00:00Z")}
	h, alice := syncedGoogle(t, g)
	h.RunDue()

	// A 410 on the token re-imports that calendar in full, dropping what Google
	// no longer has and keeping the last good copy of what it can't read.
	g.set(func(g *fakeGoogle) {
		g.gone = true
		g.events["alice@example.com"] = []map[string]any{
			gTimed("swim", "Swim", "2026-03-17T10:00:00Z", "2026-03-17T11:00:00Z"),
			gTimed("dentist", "Dentist", "garbage", "2026-03-16T11:00:00Z"),
		}
	})
	h.Clock.Advance(2 * time.Minute)
	h.RunDue()
	reqs := g.requests()
	if len(reqs) != 3 || !reqs[1].Has("syncToken") || reqs[2].Has("syncToken") || reqs[2].Get("timeMin") == "" {
		t.Fatalf("after a 410: %v", reqs)
	}
	if got := stored(t, h, alice); got != "dentist,swim" {
		t.Fatalf("after the re-import %q", got)
	}
	if d := day(t, agendaPage(t, h), "2026-03-16"); !strings.Contains(text(d), "10:00–11:00 Dentist") {
		t.Fatalf("the unreadable copy replaced the dentist: %q", d)
	}

	// Once a day, a full re-import moves the window.
	g.set(func(g *fakeGoogle) { g.gone = false })
	h.Clock.Advance(48 * time.Hour)
	h.RunDue()
	reqs = g.requests()
	if last := reqs[len(reqs)-1]; last.Has("syncToken") || last.Get("timeMin") != "2025-12-17T09:02:00Z" {
		t.Fatalf("two days on, the sync asked %v", last)
	}
	h.Clock.Advance(2 * time.Minute)
	h.RunDue()
	reqs = g.requests()
	if !reqs[len(reqs)-1].Has("syncToken") {
		t.Fatal("the next sync wasn't incremental")
	}
}

func TestGoogleReconnectNeededAndStaleNotice(t *testing.T) {
	g := newGoogle(t)
	g.events["alice@example.com"] = []map[string]any{gTimed("dentist", "Dentist", "2026-03-16T10:00:00Z", "2026-03-16T11:00:00Z")}
	h, _ := syncedGoogle(t, g)
	h.RunDue()

	// Transient failures retry every 2 minutes, without backoff, and never need
	// a reconnect; the third shows the notice.
	for i, status := range []int{http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		g.set(func(g *fakeGoogle) { g.fail = status })
		n := len(g.requests())
		h.Clock.Advance(2 * time.Minute)
		h.RunDue()
		if len(g.requests()) != n+1 {
			t.Fatalf("failure %d wasn't retried on schedule", i+1)
		}
		page := agendaPage(t, h)
		if stale := strings.Contains(page, "Alice: last updated 15 Mar 2026 09:00"); stale != (i >= 2) || strings.Contains(page, "Reconnect needed") {
			t.Fatalf("after %d failures:\n%s", i+1, page)
		}
	}
	g.set(func(g *fakeGoogle) { g.fail = 0 })
	h.Clock.Advance(2 * time.Minute)
	h.RunDue()
	if page := agendaPage(t, h); strings.Contains(page, "last updated") {
		t.Fatalf("the notice outlived a good sync:\n%s", page)
	}

	// Google withdraws access: syncing stops, the events stay.
	g.set(func(g *fakeGoogle) { g.withdrawn = true })
	h.Clock.Advance(2 * time.Minute)
	h.RunDue()
	page := agendaPage(t, h)
	if !strings.Contains(page, "Reconnect needed") || !strings.Contains(page, "Alice: last updated 15 Mar 2026 09:10") || !strings.Contains(page, "Dentist") {
		t.Fatalf("after invalid_grant:\n%s", page)
	}
	if page := settings(t, h); !strings.Contains(page, "Reconnect needed") || strings.Contains(page, "Sync failing") {
		t.Fatalf("Settings for an account needing a reconnect:\n%s", page)
	}
	tokens := g.tokens()
	h.Clock.Advance(time.Hour)
	h.RunDue()
	if g.tokens() != tokens {
		t.Fatal("an account that needs reconnecting kept syncing")
	}

	// Reconnecting resumes syncing.
	g.set(func(g *fakeGoogle) { g.withdrawn = false })
	settings(t, h)
	var acct int64
	h.App.DB.QueryRow("SELECT id FROM integrations WHERE type = 'google_calendar'").Scan(&acct)
	h.Post(t, "/integrations/google/connect", url.Values{"account": {fmt.Sprint(acct)}})
	n := len(g.requests())
	h.RunDue()
	if len(g.requests()) == n {
		t.Fatal("no sync after reconnecting")
	}
	if page := agendaPage(t, h); strings.Contains(page, "Reconnect needed") || strings.Contains(page, "last updated") {
		t.Fatalf("after reconnecting:\n%s", page)
	}
}

func TestSyncNowAndTheTwelveMonthNotice(t *testing.T) {
	g := newGoogle(t)
	h, _ := syncedGoogle(t, g)
	res, err := h.Client.Do(func() *http.Request {
		req, _ := http.NewRequest(http.MethodPost, h.Server.URL+"/calendar/sync", strings.NewReader(url.Values{"back": {"/calendar?view=week&date=2026-03-20"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		return req
	}())
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Request.URL.RequestURI() != "/calendar?view=week&date=2026-03-20" || len(g.requests()) != 1 {
		t.Fatalf("Sync now landed on %s after %d requests", res.Request.URL, len(g.requests()))
	}
	h.Post(t, "/calendar/sync", nil)
	if len(g.requests()) != 2 {
		t.Fatal("Sync now waited for the schedule")
	}
	const notice = "Google events more than 12 months ahead aren't shown"
	if _, page := h.Get(t, "/calendar?view=month&date=2027-03-01"); !strings.Contains(page, notice) {
		t.Fatalf("no notice past 12 months:\n%s", page)
	}
	if _, page := h.Get(t, "/calendar?view=month&date=2027-02-01"); strings.Contains(page, notice) {
		t.Fatal("the notice shows within 12 months")
	}
}

// A calendar Google no longer has is hidden, and the account's others still sync.
func TestGoogleCalendarGoneIsHidden(t *testing.T) {
	g := newGoogle(t)
	g.list(cal("alice@example.com", "Alice", "#9fc6e7", "owner"), cal("club", "Club", "#f59e0b", "reader"))
	h := googleHousehold(t, g)
	h.Post(t, "/integrations/google/connect", nil)
	var alice, club int64
	h.App.DB.QueryRow("SELECT id FROM source_calendars WHERE external_id = 'alice@example.com'").Scan(&alice)
	h.App.DB.QueryRow("SELECT id FROM source_calendars WHERE external_id = 'club'").Scan(&club)
	for _, id := range []int64{club, alice} {
		h.Post(t, fmt.Sprintf("/settings/google-calendars/%d", id), url.Values{"enabled": {"on"}})
	}
	g.events["alice@example.com"] = []map[string]any{gTimed("dentist", "Dentist", "2026-03-16T10:00:00Z", "2026-03-16T11:00:00Z")}
	g.missing = "club"
	h.RunDue()
	if got := stored(t, h, alice); got != "dentist" {
		t.Fatalf("the other calendar stored %q", got)
	}
	var enabled bool
	var failures int
	h.App.DB.QueryRow("SELECT enabled FROM source_calendars WHERE id = ?", club).Scan(&enabled)
	h.App.DB.QueryRow("SELECT consecutive_failures FROM integrations WHERE type = 'google_calendar'").Scan(&failures)
	if enabled || failures != 0 {
		t.Fatalf("the gone calendar: enabled %v, account failures %d", enabled, failures)
	}
}
