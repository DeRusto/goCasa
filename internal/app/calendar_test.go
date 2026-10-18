package app_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/apptest"
)

// household sets up Ada (Admin, 1), Bob (2), Carol (3) and one local calendar (1).
func household(t *testing.T) *apptest.Harness {
	t.Helper()
	h := apptest.New(t)
	setup(t, h)
	h.Post(t, "/settings/members", member("Bob", "#22c55e", "member", ""))
	h.Post(t, "/settings/members", member("Carol", "#ef4444", "member", ""))
	if code, _ := h.Post(t, "/calendar/calendars", url.Values{"name": {"Family"}, "color": {"#a855f7"}}); code != http.StatusOK {
		t.Fatalf("create calendar = %d", code)
	}
	return h
}

func timed(title, start, end, tz string, members ...string) url.Values {
	return url.Values{"calendar_id": {"1"}, "title": {title}, "start": {start}, "end": {end}, "tz": {tz}, "members": members}
}

func allDay(title, first, last string, members ...string) url.Values {
	return url.Values{"calendar_id": {"1"}, "title": {title}, "all_day": {"on"}, "start_date": {first}, "end_date": {last}, "members": members}
}

func addEvent(t *testing.T, h *apptest.Harness, f url.Values) {
	t.Helper()
	if code, body := h.Post(t, "/calendar/event", f); code != http.StatusOK {
		t.Fatalf("create event = %d %q", code, body)
	}
}

// day is the rendered section for date in a calendar page.
func day(t *testing.T, page, date string) string {
	t.Helper()
	_, rest, ok := strings.Cut(page, `data-date="`+date+`"`)
	if !ok {
		t.Fatalf("no day %s in page", date)
	}
	s, _, _ := strings.Cut(rest, "</section>")
	return s
}

func TestCreateEditDeleteTimedEvent(t *testing.T) {
	h := household(t)
	addEvent(t, h, timed("Dentist", "2026-03-15T09:00", "2026-03-15T10:00", "UTC"))
	_, page := h.Get(t, "/calendar?view=day&date=2026-03-15")
	if d := day(t, page, "2026-03-15"); !strings.Contains(d, "Dentist") || !strings.Contains(d, "09:00") {
		t.Fatalf("day view = %q", d)
	}

	if code, body := h.Get(t, "/calendar/event/1"); code != http.StatusOK || !strings.Contains(body, `value="Dentist"`) || !strings.Contains(body, `value="2026-03-15T09:00"`) {
		t.Fatalf("edit form = %d %q", code, body)
	}
	if code, _ := h.Post(t, "/calendar/event/1", timed("Orthodontist", "2026-03-16T09:00", "2026-03-16T10:00", "UTC")); code != http.StatusOK {
		t.Fatalf("update = %d", code)
	}
	_, page = h.Get(t, "/calendar?view=week&date=2026-03-15")
	if strings.Contains(day(t, page, "2026-03-15"), "Orthodontist") || !strings.Contains(day(t, page, "2026-03-16"), "Orthodontist") {
		t.Fatal("moved event is not on its new day")
	}

	if code, _ := h.Post(t, "/calendar/event/1/delete", nil); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}
	if _, page = h.Get(t, "/calendar?view=week&date=2026-03-15"); strings.Contains(page, "Orthodontist") {
		t.Fatal("deleted event still shows")
	}
	if code, _ := h.Get(t, "/calendar/event/1"); code != http.StatusNotFound {
		t.Fatalf("deleted event form = %d, want 404", code)
	}
}

func TestAllDayEventsCoverTheirDatesInclusive(t *testing.T) {
	h := household(t)
	addEvent(t, h, allDay("Camping", "2026-03-14", "2026-03-16"))
	var date, end string
	h.App.DB.QueryRow("SELECT all_day_date, all_day_end_date FROM events").Scan(&date, &end)
	if date != "2026-03-14" || end != "2026-03-17" {
		t.Fatalf("stored %s..%s, want 2026-03-14..2026-03-17 (exclusive)", date, end)
	}
	_, page := h.Get(t, "/calendar?view=week&date=2026-03-15") // Sun 15 – Sat 21
	for d, want := range map[string]bool{"2026-03-15": true, "2026-03-16": true, "2026-03-17": false} {
		if got := strings.Contains(day(t, page, d), "Camping"); got != want {
			t.Errorf("%s shows Camping = %v, want %v", d, got, want)
		}
	}
	_, page = h.Get(t, "/calendar?view=day&date=2026-03-14") // the previous week
	if !strings.Contains(day(t, page, "2026-03-14"), "All day") {
		t.Error("first day missing")
	}
	if code, body := h.Get(t, "/calendar/event/1"); code != http.StatusOK || !strings.Contains(body, `name="end_date" value="2026-03-16"`) {
		t.Fatalf("edit form shows the wrong last day: %q", body)
	}
	if code, _ := h.Post(t, "/calendar/event", allDay("Backwards", "2026-03-16", "2026-03-15")); code != http.StatusBadRequest {
		t.Fatalf("last day before first = %d, want 400", code)
	}
}

func TestEventsAtMidnightStayOnTheirDay(t *testing.T) {
	h := household(t)
	addEvent(t, h, timed("Early", "2026-03-16T00:00", "2026-03-16T01:00", "UTC"))
	addEvent(t, h, timed("Late", "2026-03-16T23:59", "2026-03-17T00:00", "UTC"))
	addEvent(t, h, timed("Overnight", "2026-03-16T23:00", "2026-03-17T02:00", "UTC"))
	_, page := h.Get(t, "/calendar?view=week&date=2026-03-16")
	mon, tue, sun := day(t, page, "2026-03-16"), day(t, page, "2026-03-17"), day(t, page, "2026-03-15")
	if !strings.Contains(mon, "00:00–01:00 Early") || !strings.Contains(mon, "23:59–00:00 Late") || strings.Contains(sun, "Early") {
		t.Errorf("Monday = %q", mon)
	}
	if strings.Contains(tue, "Early") || strings.Contains(tue, "Late") {
		t.Errorf("Tuesday has Monday's midnight events: %q", tue)
	}
	if !strings.Contains(mon, "23:00–… Overnight") || !strings.Contains(tue, "…–02:00 Overnight") {
		t.Errorf("overnight event: Monday %q, Tuesday %q", mon, tue)
	}
	if code, _ := h.Post(t, "/calendar/event", timed("Zero", "2026-03-16T09:00", "2026-03-16T09:00", "UTC")); code != http.StatusBadRequest {
		t.Fatalf("end at start = %d, want 400", code)
	}
}

func TestTimesUseTheEventsZone(t *testing.T) {
	h := household(t) // Household zone is UTC
	// New York is on EDT (UTC-4) from 8 March 2026; 23:30 there is 03:30 UTC the next day.
	addEvent(t, h, timed("Call", "2026-03-15T23:30", "2026-03-16T00:30", "America/New_York"))
	_, page := h.Get(t, "/calendar?view=week&date=2026-03-15")
	if d := day(t, page, "2026-03-16"); !strings.Contains(d, "03:30–04:30 Call") {
		t.Fatalf("Monday = %q", d)
	}
	if strings.Contains(day(t, page, "2026-03-15"), "Call") {
		t.Fatal("event shows on its New York date, not the Household's")
	}
	if _, body := h.Get(t, "/calendar/event/1"); !strings.Contains(body, `value="2026-03-15T23:30"`) || !strings.Contains(body, `value="America/New_York"`) {
		t.Fatalf("edit form is not in the event's zone: %q", body)
	}
	if code, _ := h.Post(t, "/calendar/event", timed("Nowhere", "2026-03-15T09:00", "2026-03-15T10:00", "Mars/Olympus")); code != http.StatusBadRequest {
		t.Fatalf("unknown zone = %d, want 400", code)
	}
}

func TestMemberFilterAlwaysShowsHouseholdEventsAndColoursFollowMembers(t *testing.T) {
	h := household(t)
	addEvent(t, h, timed("Dinner", "2026-03-15T18:00", "2026-03-15T19:00", "UTC"))        // Household
	addEvent(t, h, timed("Football", "2026-03-15T10:00", "2026-03-15T11:00", "UTC", "2")) // Bob
	addEvent(t, h, timed("Piano", "2026-03-15T12:00", "2026-03-15T13:00", "UTC", "3"))    // Carol
	addEvent(t, h, timed("Zoo", "2026-03-15T14:00", "2026-03-15T15:00", "UTC", "2", "3")) // both

	_, page := h.Get(t, "/calendar?view=day&date=2026-03-15")
	d := day(t, page, "2026-03-15")
	for _, title := range []string{"Dinner", "Football", "Piano", "Zoo"} {
		if !strings.Contains(d, title) {
			t.Errorf("unfiltered day lacks %s", title)
		}
	}
	item := func(d, title string) string {
		i := strings.Index(d, title)
		return d[strings.LastIndex(d[:i], "<li>"):i]
	}
	if s := item(d, "Dinner"); !strings.Contains(s, "#a855f7") {
		t.Errorf("Household event not in the calendar colour: %q", s)
	}
	if s := item(d, "Football"); !strings.Contains(s, "#22c55e") || strings.Contains(s, "#a855f7") {
		t.Errorf("Bob's event not in Bob's colour: %q", s)
	}
	if s := item(d, "Zoo"); !strings.Contains(s, "#22c55e") || !strings.Contains(s, "#ef4444") {
		t.Errorf("shared event lacks a Member's colour: %q", s)
	}

	_, page = h.Get(t, "/calendar?view=day&date=2026-03-15&users=2")
	d = day(t, page, "2026-03-15")
	if !strings.Contains(d, "Dinner") || !strings.Contains(d, "Football") || !strings.Contains(d, "Zoo") || strings.Contains(d, "Piano") {
		t.Errorf("users=2 day = %q", d)
	}
	_, page = h.Get(t, "/calendar?view=day&date=2026-03-15&users=2,3")
	if !strings.Contains(day(t, page, "2026-03-15"), "Piano") {
		t.Error("users=2,3 hides Carol's event")
	}
	if !strings.Contains(page, "users=2%2C3") {
		t.Error("navigation links drop the filter")
	}
}

func TestMonthAndAgendaViewsAndDeepLinks(t *testing.T) {
	h := household(t)
	addEvent(t, h, timed("Haircut", "2026-04-30T09:00", "2026-04-30T10:00", "UTC"))
	addEvent(t, h, allDay("Holiday", "2026-05-01", "2026-05-03"))

	_, page := h.Get(t, "/calendar?view=month&date=2026-04-10")
	if !strings.Contains(page, "April 2026") || !strings.Contains(day(t, page, "2026-04-30"), "Haircut") {
		t.Fatal("month view misses its last day's event")
	}
	// April 2026 starts on a Wednesday and ends on a Thursday: the grid runs Sun 29 March – Sat 2 May.
	if !strings.Contains(day(t, page, "2026-05-02"), "Holiday") || strings.Contains(page, `data-date="2026-05-03"`) || !strings.Contains(page, `data-date="2026-03-29"`) {
		t.Fatal("month grid has the wrong days")
	}

	_, page = h.Get(t, "/calendar?view=agenda&date=2026-04-30")
	if !strings.Contains(day(t, page, "2026-04-30"), "Haircut") || !strings.Contains(day(t, page, "2026-05-03"), "Holiday") {
		t.Fatal("agenda misses events")
	}
	if strings.Contains(page, `data-date="2026-05-04"`) {
		t.Fatal("agenda shows an empty day")
	}

	// No date: today on the fake clock (15 March 2026); a bad date falls back to today too.
	for _, path := range []string{"/calendar?view=day", "/calendar?view=day&date=nonsense", "/calendar"} {
		if code, page := h.Get(t, path); code != http.StatusOK || !strings.Contains(page, `data-date="2026-03-15"`) {
			t.Errorf("%s did not open on today", path)
		}
	}
}

func TestArchivedMembersStayOnEventsTheyAreHiddenFrom(t *testing.T) {
	h := household(t)
	addEvent(t, h, timed("Vet", "2026-03-15T09:00", "2026-03-15T10:00", "UTC", "2", "3"))
	h.Post(t, "/settings/members/3/archive", nil)
	if _, body := h.Get(t, "/calendar/event/1"); strings.Contains(body, "Carol") {
		t.Fatal("archived Member is offered in the picker")
	}
	h.Post(t, "/calendar/event/1", timed("Vet", "2026-03-15T09:00", "2026-03-15T10:00", "UTC", "2"))
	_, page := h.Get(t, "/calendar?view=day&date=2026-03-15&users=3")
	if !strings.Contains(day(t, page, "2026-03-15"), "#ef4444") {
		t.Fatal("editing dropped the archived Member from the event")
	}
	if code, _ := h.Post(t, "/settings/members/3/delete", nil); code != http.StatusConflict {
		t.Fatalf("delete a Member with events = %d, want 409", code)
	}
}

func TestDSTGapIsRefusedAndOverlapTakesTheFirstOffset(t *testing.T) {
	h := household(t)
	// Los Angeles springs forward 02:00→03:00 on 8 March 2026 and falls back 02:00→01:00 on 1 November.
	if code, _ := h.Post(t, "/calendar/event", timed("Gap", "2026-03-08T02:30", "2026-03-08T03:30", "America/Los_Angeles")); code != http.StatusBadRequest {
		t.Fatalf("time in the gap = %d, want 400", code)
	}
	// London falls back 02:00→01:00 on 25 October 2026, and Go alone picks the later 01:30 there.
	for i, c := range []struct {
		tz, start, end string
		want           time.Time
	}{
		{"America/Los_Angeles", "2026-11-01T01:30", "2026-11-01T03:00", time.Date(2026, 11, 1, 8, 30, 0, 0, time.UTC)}, // 01:30 PDT
		{"Europe/London", "2026-10-25T01:30", "2026-10-25T03:00", time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)},      // 01:30 BST
	} {
		addEvent(t, h, timed("Overlap", c.start, c.end, c.tz))
		var start int64
		h.App.DB.QueryRow("SELECT start_utc FROM events WHERE id = ?", i+1).Scan(&start)
		if start != c.want.Unix() {
			t.Errorf("%s overlap start = %v, want %v", c.tz, time.Unix(start, 0).UTC(), c.want)
		}
	}
}

func TestIncompleteEventRowsAreRefused(t *testing.T) {
	h := household(t)
	for _, q := range []string{
		"INSERT INTO events (calendar_id, title, all_day, updated_at) VALUES (1, 'x', 1, 0)",
		"INSERT INTO events (calendar_id, title, all_day, start_tzid, updated_at) VALUES (1, 'x', 0, 'UTC', 0)",
	} {
		if _, err := h.App.DB.Exec(q); err == nil {
			t.Errorf("accepted %s", q)
		}
	}
}

func TestTitleLengthCap(t *testing.T) {
	h := household(t)
	addEvent(t, h, timed(strings.Repeat("é", 200), "2026-03-15T09:00", "2026-03-15T10:00", "UTC"))
	for _, title := range []string{strings.Repeat("é", 201), "  "} {
		if code, _ := h.Post(t, "/calendar/event", timed(title, "2026-03-15T09:00", "2026-03-15T10:00", "UTC")); code != http.StatusBadRequest {
			t.Errorf("title of %d runes = %d, want 400", len([]rune(title)), code)
		}
	}
}
