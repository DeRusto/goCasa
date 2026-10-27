package app_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/app"
	"github.com/DeRusto/goCasa/internal/apptest"
)

// These run with the Household in Auckland and the server in UTC, so a path
// that takes the server's zone for the Household's shows.
var auckland = apptest.WithHouseholdTimezone("Pacific/Auckland")

// An all-day event's dialog offers the Household's zone for switching to timed.
func TestAllDayFormDefaultsToTheHouseholdZone(t *testing.T) {
	t.Parallel()
	h := household(t, auckland)
	addEvent(t, h, allDay("Camping", "2026-03-14", "2026-03-16"))
	if _, body := h.Get(t, "/calendar/event/1"); !strings.Contains(body, `value="Pacific/Auckland"`) {
		t.Fatalf("an all-day event's dialog isn't in the household zone: %q", body)
	}
}

// A feed's floating time is a wall time in the Household's zone.
func TestFeedFloatingTimeIsInTheHouseholdZone(t *testing.T) {
	t.Parallel()
	h := household(t, auckland)
	addFeed(t, h, "School", newFeed(t, vcal(vevent("UID:lunch@school", "SUMMARY:Lunch", "DTSTART:20260320T100000", "DTEND:20260320T110000"))).URL)
	if d := day(t, agendaPage(t, h), "2026-03-20"); !strings.Contains(text(d), "10:00–11:00 Lunch") {
		t.Fatalf("a floating 10:00 in Auckland = %q", d)
	}
}

// A Google event with no zone, on a calendar with none, is in the Household's.
func TestGoogleEventWithNoZoneIsInTheHouseholdZone(t *testing.T) {
	t.Parallel()
	g := newGoogle(t)
	g.noZone = true
	g.events["alice@example.com"] = []map[string]any{gTimed("dentist", "Dentist", "2026-03-16T10:00:00Z", "2026-03-16T11:00:00Z")}
	h, _ := syncedGoogle(t, g, auckland)
	h.RunDue()
	if _, page := h.Get(t, fmt.Sprintf("/calendar/synced/%d", eventID(t, h, "dentist"))); !strings.Contains(page, `name="tz" value="Pacific/Auckland"`) {
		t.Fatalf("a zoneless Google event isn't in the household zone:\n%s", page)
	}
}

// The sync window's all-day edges are the Household's dates: at 01:00 in
// Auckland (12:00 UTC), an all-day event on the day before the window goes.
func TestGoogleWindowDatesAreTheHouseholds(t *testing.T) {
	t.Parallel()
	g := newGoogle(t)
	g.events["alice@example.com"] = []map[string]any{gAllDay("fair", "Fair", "2025-12-15", "2025-12-16"), gAllDay("fete", "Fete", "2025-12-16", "2025-12-17")}
	h, alice := syncedGoogle(t, g, auckland)
	h.Clock.Set(time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)) // the window opens 16 December, 01:00 in Auckland
	h.RunDue()
	if got := stored(t, h, alice); got != "fete" {
		t.Fatalf("stored %q, want the fete alone", got)
	}
}

// The daily full re-import falls on the Household's next day. The time in the
// day is random, so check several: each lands in the wrong day half the time.
func TestGoogleFullReimportIsTheHouseholdsNextDay(t *testing.T) {
	t.Parallel()
	g := newGoogle(t)
	h, _ := syncedGoogle(t, g, auckland)
	nz, _ := time.LoadLocation("Pacific/Auckland")
	for range 8 {
		h.RunDue()
		var next int64
		h.App.DB.QueryRow("SELECT sync_cursor ->> '$.next_full' FROM integrations WHERE type = 'google_calendar'").Scan(&next)
		now := h.Clock.Now().In(nz)
		day := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, nz)
		if got := time.Unix(next, 0); got.Before(day) || !got.Before(day.AddDate(0, 0, 1)) {
			t.Fatalf("at %v the next full re-import is %v, not on %v", now, got.In(nz), day.Format(time.DateOnly))
		}
		h.Clock.Set(time.Unix(next, 0))
	}
}

// A Google Series master with no zone of its own is in the Household's: a
// Split counts its COUNT and moves its EXDATEs by Auckland's wall times, which
// shift an hour against UTC when New Zealand summer time ends on 5 April.
func TestGoogleZonelessSeriesSplitsInTheHouseholdZone(t *testing.T) {
	t.Parallel()
	g := newGoogle(t)
	g.noZone = true
	var occs []map[string]any
	for i, s := range []string{"2026-03-14T10", "2026-03-21T10", "2026-03-28T10", "2026-04-04T10", "2026-04-11T11", "2026-04-25T11"} { // 23:00 in Auckland
		t0, _ := time.Parse(time.RFC3339, s+":00:00Z")
		id, start := "quiz_"+t0.Format("20060102"), t0.Format(time.RFC3339)
		o := withEtag(gOccurrence("quiz", id, "Quiz", start, t0.Add(time.Hour).Format(time.RFC3339)), fmt.Sprintf("o%d", i))
		o["originalStartTime"] = map[string]any{"dateTime": start}
		occs = append(occs, o)
		g.got[id], g.etags[id] = o, o["etag"].(string)
	}
	master := withEtag(gTimed("quiz", "Quiz", "2026-03-14T10:00:00Z", "2026-03-14T11:00:00Z"), "m1")
	master["recurrence"] = []string{"RRULE:FREQ=WEEKLY;COUNT=10", "EXDATE;TZID=Pacific/Auckland:20260418T230000"}
	g.got["quiz"], g.etags["quiz"] = master, "m1"
	g.events["alice@example.com"] = occs
	h, _ := syncedGoogle(t, g, auckland)
	h.RunDue()

	split := func(id, start, end string) []any {
		t.Helper()
		n := len(g.sent())
		path := fmt.Sprintf("/calendar/synced/%d", eventID(t, h, id))
		if code, page := submit(t, h, path, "/edit", googleEdit("Quiz", start, end, "scope", "following")); code != http.StatusOK {
			t.Fatalf("splitting at %s = %d:\n%s", id, code, page)
		}
		w := g.sent()[n:]
		if len(w) == 0 || w[0].method != http.MethodPost {
			t.Fatalf("splitting at %s sent %+v", id, w)
		}
		return w[0].body["recurrence"].([]any)
	}
	// 23:00 on 21 March in Auckland to 09:00 in London moves 18 April's 23:00 to 09:00.
	if got := fmt.Sprint(split("quiz_20260321", "2026-03-21T09:00", "2026-03-21T10:00")); got != "[RRULE:FREQ=WEEKLY;COUNT=9 EXDATE;TZID=Europe/London:20260418T090000]" {
		t.Fatalf("a Split on 21 March sent %s", got)
	}
	// Four Occurrences come before 11 April's 23:00 in Auckland (11:00 UTC); five come before it at 10:00 UTC.
	if got := fmt.Sprint(split("quiz_20260411", "2026-04-11T09:00", "2026-04-11T10:00")); !strings.Contains(got, "COUNT=6") {
		t.Fatalf("a Split on 11 April sent %s", got)
	}
}

// One request reads household_settings once, however many parts of the page
// follow the Household: a Wall display's Settings and screensaver.
func TestARequestReadsTheHouseholdOnce(t *testing.T) {
	t.Parallel()
	h := household(t, auckland)
	post(t, h, "/device/wall-display", url.Values{"wall_display": {"on"}})
	for _, page := range []string{"/screensaver", "/settings", "/today", "/calendar", "/chores"} {
		before := app.HouseholdReads(h.App)
		if code, _ := h.Get(t, page); code != http.StatusOK {
			t.Fatalf("%s = %d", page, code)
		}
		if n := app.HouseholdReads(h.App) - before; n != 1 {
			t.Errorf("%s read the household %d times, want 1", page, n)
		}
	}
}

// aucklandMonday is 01:00 on Monday 16 March in Auckland: still Sunday the
// 15th, 12:00, in UTC.
var aucklandMonday = time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)

// Today, the calendar, Chores and the screensaver keep the Household's day.
func TestPagesKeepTheHouseholdsDay(t *testing.T) {
	t.Parallel()
	h := household(t, auckland)
	post(t, h, "/chores", chore("Dishes", "daily", "each", "5", "2"))
	post(t, h, "/device/wall-display", url.Values{"wall_display": {"on"}})
	h.Clock.Set(aucklandMonday)

	if _, page := h.Get(t, "/today"); !strings.Contains(page, "Monday 16 March") || !strings.Contains(page, "01:00") {
		t.Errorf("Today isn't Monday 01:00 in Auckland:\n%s", page)
	}
	if _, page := h.Get(t, "/calendar?view=day"); !strings.Contains(page, `data-date="2026-03-16"`) {
		t.Errorf("the calendar didn't open on Monday 16 March:\n%s", page)
	}
	if b := choreBoard(t, h); !strings.Contains(b, `data-round="2026-03-16"`) {
		t.Errorf("the chore board has no Round for Monday 16 March: %q", b)
	}
	if code := complete(h, t, "1", "2", "2026-03-16"); code != http.StatusOK {
		t.Errorf("completing Monday's Round = %d, want 200", code)
	}
	// Night mode is 22:00 to 06:00 in Auckland: off at noon there, 23:00 in UTC.
	nightMode(t, h, "black")
	h.Clock.Set(time.Date(2026, 3, 15, 23, 0, 0, 0, time.UTC))
	if _, page := h.Get(t, "/screensaver"); !strings.Contains(page, ">12:00</time>") || !strings.Contains(page, "Monday 16 March") || !strings.Contains(page, holdsWakeLock) {
		t.Errorf("the screensaver isn't Monday noon in Auckland, out of Night mode:\n%s", page)
	}
}
