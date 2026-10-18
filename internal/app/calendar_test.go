package app_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
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

func repeating(f url.Values, repeat string, extra ...string) url.Values {
	f.Set("repeat", repeat)
	for i := 0; i+1 < len(extra); i += 2 {
		f.Add(extra[i], extra[i+1])
	}
	return f
}

// occurrence is the path of a Series' Occurrence that starts at original.
func occurrence(id int, original time.Time, action string) string {
	return "/calendar/event/" + strconv.Itoa(id) + action + "?occurrence=" + strconv.FormatInt(original.Unix(), 10)
}

func TestPresetSeriesRepeat(t *testing.T) {
	h := household(t)
	// Sunday 15 March 2026; weekly on Mondays and Wednesdays, every other week.
	addEvent(t, h, repeating(timed("Swim", "2026-03-16T07:00", "2026-03-16T08:00", "UTC"), "weekly", "interval", "2", "byday", "MO", "byday", "WE"))
	addEvent(t, h, repeating(allDay("Bins", "2026-03-15", "2026-03-15"), "daily", "interval", "3"))
	_, page := h.Get(t, "/calendar?view=month&date=2026-03-15")
	for d, want := range map[string]bool{"2026-03-16": true, "2026-03-18": true, "2026-03-23": false, "2026-03-30": true, "2026-03-13": false} {
		if got := strings.Contains(day(t, page, d), "07:00–08:00 Swim"); got != want {
			t.Errorf("%s shows Swim = %v, want %v", d, got, want)
		}
	}
	for d, want := range map[string]bool{"2026-03-15": true, "2026-03-16": false, "2026-03-18": true, "2026-03-21": true, "2026-03-14": false} {
		if got := strings.Contains(day(t, page, d), "Bins"); got != want {
			t.Errorf("%s shows Bins = %v, want %v", d, got, want)
		}
	}
	if !strings.Contains(page, occurrence(1, time.Date(2026, 3, 30, 7, 0, 0, 0, time.UTC), "")) {
		t.Error("an Occurrence does not link to its own dialog")
	}
	for _, f := range []url.Values{
		repeating(timed("x", "2026-03-16T07:00", "2026-03-16T08:00", "UTC"), "weekly", "interval", "0"),
		repeating(timed("x", "2026-03-16T07:00", "2026-03-16T08:00", "UTC"), "weekly", "byday", "XX"),
		repeating(timed("x", "2026-03-16T07:00", "2026-03-16T08:00", "UTC"), "fortnightly"),
	} {
		if code, _ := h.Post(t, "/calendar/event", f); code != http.StatusBadRequest {
			t.Errorf("%v = %d, want 400", f, code)
		}
	}
}

func TestCustomRule(t *testing.T) {
	h := household(t)
	// Second Tuesday of each month, not in April, plus an extra on Thursday 19 March.
	addEvent(t, h, repeating(timed("Book club", "2026-03-10T19:00", "2026-03-10T21:00", "Europe/London"), "custom",
		"rrule", "RRULE:FREQ=MONTHLY;BYDAY=2TU\nEXDATE:20260414T190000\nRDATE:20260319T190000"))
	_, page := h.Get(t, "/calendar?view=agenda&date=2026-03-01")
	_, page2 := h.Get(t, "/calendar?view=agenda&date=2026-03-31")
	all := page + page2
	for _, d := range []string{"2026-03-10", "2026-03-19"} {
		if !strings.Contains(day(t, page, d), "19:00–21:00 Book club") {
			t.Errorf("%s lacks Book club", d)
		}
	}
	if strings.Contains(all, `data-date="2026-04-14"`) {
		t.Error("EXDATE still shows")
	}
	if _, body := h.Get(t, "/calendar/event/1"); !strings.Contains(body, "FREQ=MONTHLY;BYDAY=2TU") || !strings.Contains(body, "EXDATE:20260414T190000") {
		t.Errorf("dialog loses the custom rule: %q", body)
	}
	for _, rule := range []string{"RRULE:FREQ=FORTNIGHTLY", "RRULE:FREQ=HOURLY", "RRULE:FREQ=DAILY;BYHOUR=9,17", "RRULE:FREQ=DAILY\nEXDATE:tomorrow", "SOMETHING:ELSE"} {
		if code, _ := h.Post(t, "/calendar/event", repeating(timed("x", "2026-03-10T19:00", "2026-03-10T20:00", "UTC"), "custom", "rrule", rule)); code != http.StatusBadRequest {
			t.Errorf("rule %q = %d, want 400", rule, code)
		}
	}
}

func TestThisEventEditAndDelete(t *testing.T) {
	h := household(t)
	addEvent(t, h, repeating(timed("Walk", "2026-03-15T09:00", "2026-03-15T10:00", "UTC", "2"), "daily"))
	at := func(d int) time.Time { return time.Date(2026, 3, d, 9, 0, 0, 0, time.UTC) }

	if code, body := h.Get(t, occurrence(1, at(16), "")); code != http.StatusOK || !strings.Contains(body, `value="2026-03-16T09:00"`) || !strings.Contains(body, "This event") {
		t.Fatalf("Occurrence dialog = %d %q", code, body)
	}
	moved := timed("Long walk", "2026-03-16T11:00", "2026-03-16T13:00", "UTC", "3")
	if code, body := h.Post(t, occurrence(1, at(16), ""), moved); code != http.StatusOK {
		t.Fatalf("This event edit = %d %q", code, body)
	}
	if code, _ := h.Post(t, occurrence(1, at(17), "/delete"), nil); code != http.StatusOK {
		t.Fatalf("This event delete = %d", code)
	}
	_, page := h.Get(t, "/calendar?view=week&date=2026-03-15")
	if d := day(t, page, "2026-03-16"); !strings.Contains(d, "11:00–13:00 Long walk") || strings.Contains(d, "Walk<") || strings.Contains(d, "09:00") {
		t.Errorf("edited Occurrence: %q", d)
	}
	if d := day(t, page, "2026-03-17"); strings.Contains(d, "Walk") {
		t.Errorf("deleted Occurrence still shows: %q", d)
	}
	for _, d := range []string{"2026-03-15", "2026-03-18", "2026-03-21"} {
		if !strings.Contains(day(t, page, d), "09:00–10:00 Walk") {
			t.Errorf("%s lost its Occurrence", d)
		}
	}
	// Members follow the One-off edit: Carol's filter shows only the edited day.
	_, page = h.Get(t, "/calendar?view=week&date=2026-03-15&users=3")
	if !strings.Contains(day(t, page, "2026-03-16"), "Long walk") || strings.Contains(day(t, page, "2026-03-18"), "Walk") {
		t.Error("Member filter ignores the One-off edit's Members")
	}

	// Editing it again updates the same One-off edit row.
	if _, body := h.Get(t, occurrence(1, at(16), "")); !strings.Contains(body, `value="Long walk"`) || !strings.Contains(body, `value="2026-03-16T11:00"`) {
		t.Fatalf("dialog does not show the One-off edit: %q", body)
	}
	h.Post(t, occurrence(1, at(16), ""), timed("Longer walk", "2026-03-16T11:00", "2026-03-16T14:00", "UTC"))
	var rows int
	h.App.DB.QueryRow("SELECT count(*) FROM events WHERE recurring_master_id = 1 AND original_start_utc = ?", at(16).Unix()).Scan(&rows)
	if rows != 1 {
		t.Fatalf("%d One-off edit rows for one Occurrence, want 1", rows)
	}

	// A time the Series never has is not an Occurrence; an edit row is not a dialog of its own.
	if code, _ := h.Get(t, occurrence(1, at(16).Add(time.Hour), "")); code != http.StatusNotFound {
		t.Errorf("made-up Occurrence = %d, want 404", code)
	}
	if code, _ := h.Post(t, occurrence(1, time.Date(2026, 3, 14, 9, 0, 0, 0, time.UTC), "/delete"), nil); code != http.StatusNotFound {
		t.Errorf("Occurrence before the Series = %d, want 404", code)
	}
	if code, _ := h.Get(t, "/calendar/event/2"); code != http.StatusNotFound {
		t.Errorf("One-off edit row's own dialog = %d, want 404", code)
	}
}

func TestSeriesWhoseFirstDayIsNotAnOccurrence(t *testing.T) {
	h := household(t)
	// Starts on Sunday 15 March but repeats on Mondays: the first Occurrence is the 16th.
	addEvent(t, h, repeating(timed("Gym", "2026-03-15T07:00", "2026-03-15T08:00", "UTC"), "weekly", "byday", "MO"))
	if code, body := h.Get(t, "/calendar/event/1"); code != http.StatusOK || !strings.Contains(body, `value="2026-03-16T07:00"`) {
		t.Fatalf("Series dialog = %d, want its first Occurrence", code)
	}
	_, page := h.Get(t, "/calendar?view=week&date=2026-03-15")
	if strings.Contains(day(t, page, "2026-03-15"), "Gym") {
		t.Error("DTSTART shows though BYDAY leaves it out")
	}
}

func TestOccurrenceMovedBeforeItsSeriesStillShows(t *testing.T) {
	h := household(t)
	addEvent(t, h, repeating(timed("Choir", "2026-12-07T19:00", "2026-12-07T20:00", "UTC"), "weekly"))
	h.Post(t, occurrence(1, time.Date(2026, 12, 7, 19, 0, 0, 0, time.UTC), ""), timed("Choir (early)", "2026-10-20T19:00", "2026-10-20T20:00", "UTC"))
	if _, page := h.Get(t, "/calendar?view=day&date=2026-10-20"); !strings.Contains(day(t, page, "2026-10-20"), "Choir (early)") {
		t.Fatal("an Occurrence moved before its Series' start is missing")
	}
}

func TestFormErrorKeepsTheRepeat(t *testing.T) {
	h := household(t)
	f := repeating(timed("", "2026-03-10T19:00", "2026-03-10T20:00", "UTC"), "custom", "rrule", "RRULE:FREQ=MONTHLY;BYDAY=2TU")
	code, body := h.Post(t, "/calendar/event", f)
	if code != http.StatusBadRequest || !strings.Contains(body, "FREQ=MONTHLY;BYDAY=2TU") || !strings.Contains(body, `<option value="custom" selected`) {
		t.Fatalf("title error = %d, lost the repeat", code)
	}
}

func TestThisAndFollowingDeleteEndsTheSeries(t *testing.T) {
	h := household(t)
	addEvent(t, h, repeating(timed("Walk", "2026-03-15T09:00", "2026-03-15T10:00", "UTC"), "daily"))
	at := func(d int) time.Time { return time.Date(2026, 3, d, 9, 0, 0, 0, time.UTC) }
	h.Post(t, occurrence(1, at(19), ""), timed("Late walk", "2026-03-19T18:00", "2026-03-19T19:00", "UTC"))
	if code, _ := h.Post(t, occurrence(1, at(17), "/delete"), url.Values{"scope": {"following"}}); code != http.StatusOK {
		t.Fatalf("This and following delete = %d", code)
	}
	_, page := h.Get(t, "/calendar?view=week&date=2026-03-15")
	for d, want := range map[string]bool{"2026-03-15": true, "2026-03-16": true, "2026-03-17": false, "2026-03-19": false, "2026-03-21": false} {
		if got := strings.Contains(day(t, page, d), "walk") || strings.Contains(day(t, page, d), "Walk"); got != want {
			t.Errorf("%s shows Walk = %v, want %v", d, got, want)
		}
	}
	// On the first Occurrence it deletes the whole Series.
	if code, _ := h.Post(t, occurrence(1, at(15), "/delete"), url.Values{"scope": {"following"}}); code != http.StatusOK {
		t.Fatalf("delete from the first Occurrence = %d", code)
	}
	if code, _ := h.Get(t, "/calendar/event/1"); code != http.StatusNotFound {
		t.Errorf("Series after deleting from its first Occurrence = %d, want 404", code)
	}
}

func TestAllEventsDeleteRemovesTheSeries(t *testing.T) {
	h := household(t)
	addEvent(t, h, repeating(allDay("Bins", "2026-03-15", "2026-03-15"), "weekly"))
	if code, _ := h.Post(t, "/calendar/event/1/delete?occurrence="+strconv.FormatInt(time.Date(2026, 3, 22, 0, 0, 0, 0, time.UTC).Unix(), 10), url.Values{"scope": {"all"}}); code != http.StatusOK {
		t.Fatalf("All events delete = %d", code)
	}
	if _, page := h.Get(t, "/calendar?view=month&date=2026-03-15"); strings.Contains(page, "Bins") {
		t.Error("All events delete left Occurrences")
	}
}

func TestSplitResetsLaterOneOffEditsAfterAWarning(t *testing.T) {
	h := household(t)
	addEvent(t, h, repeating(timed("Walk", "2026-03-15T09:00", "2026-03-15T10:00", "UTC", "2"), "daily"))
	at := func(d int) time.Time { return time.Date(2026, 3, d, 9, 0, 0, 0, time.UTC) }
	h.Post(t, occurrence(1, at(16), ""), timed("Early walk", "2026-03-16T06:00", "2026-03-16T07:00", "UTC")) // before the Split: kept
	h.Post(t, occurrence(1, at(18), ""), timed("Late walk", "2026-03-18T18:00", "2026-03-18T19:00", "UTC"))
	h.Post(t, occurrence(1, at(20), "/delete"), nil)

	if _, body := h.Get(t, occurrence(1, at(17), "")); !strings.Contains(body, `value="following"`) || !strings.Contains(body, `value="all"`) {
		t.Fatalf("dialog has no scope picker: %q", body)
	}
	run := repeating(timed("Run", "2026-03-17T07:00", "2026-03-17T08:00", "UTC", "3"), "daily")
	run.Set("scope", "following")
	code, body := h.Post(t, occurrence(1, at(17), ""), run)
	if code != http.StatusConflict || !strings.Contains(body, "2 One-off edits") {
		t.Fatalf("Split without the warning = %d %q", code, body)
	}
	run.Set("reset", "2")
	if code, body := h.Post(t, occurrence(1, at(17), ""), run); code != http.StatusOK {
		t.Fatalf("confirmed Split = %d %q", code, body)
	}
	_, page := h.Get(t, "/calendar?view=week&date=2026-03-15")
	for d, want := range map[string]string{"2026-03-15": "09:00–10:00 Walk", "2026-03-16": "06:00–07:00 Early walk", "2026-03-17": "07:00–08:00 Run", "2026-03-18": "07:00–08:00 Run", "2026-03-20": "07:00–08:00 Run"} {
		if s := day(t, page, d); !strings.Contains(s, want) || strings.Count(s, "<li>") != 1 {
			t.Errorf("%s = %q, want only %q", d, s, want)
		}
	}
	// The halves aren't linked: the new Series is its own event and the old one ends.
	if _, body := h.Get(t, "/calendar/event/1"); !strings.Contains(body, "UNTIL=20260317T085959Z") {
		t.Errorf("old Series does not end before the Split: %q", body)
	}
	if !strings.Contains(page, `href="/calendar/event/5?occurrence=`) {
		t.Error("the new Series is not its own event")
	}
}

func TestSplitOnTheFirstOccurrenceCoversEverything(t *testing.T) {
	h := household(t)
	addEvent(t, h, repeating(allDay("Bins", "2026-03-15", "2026-03-15"), "weekly"))
	first := "/calendar/event/1?occurrence=" + strconv.FormatInt(time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC).Unix(), 10)
	f := repeating(allDay("Recycling", "2026-03-16", "2026-03-16"), "weekly")
	f.Set("scope", "following")
	if code, body := h.Post(t, first, f); code != http.StatusOK {
		t.Fatalf("Split on the first Occurrence = %d %q", code, body)
	}
	_, page := h.Get(t, "/calendar?view=month&date=2026-03-15")
	if strings.Contains(page, "Bins") || !strings.Contains(day(t, page, "2026-03-16"), "Recycling") || !strings.Contains(day(t, page, "2026-03-23"), "Recycling") {
		t.Errorf("Split on the first Occurrence left the old Series: %q", page)
	}
	var n int
	h.App.DB.QueryRow("SELECT count(*) FROM events").Scan(&n)
	if n != 1 {
		t.Errorf("%d event rows, want the one Series", n)
	}
}

func TestAllEventsEditKeepsOneOffEditsOwnChanges(t *testing.T) {
	h := household(t)
	addEvent(t, h, repeating(timed("Walk", "2026-03-15T09:00", "2026-03-15T10:00", "UTC", "2"), "daily"))
	at := func(d int) time.Time { return time.Date(2026, 3, d, 9, 0, 0, 0, time.UTC) }
	h.Post(t, occurrence(1, at(16), ""), timed("Long walk", "2026-03-16T09:00", "2026-03-16T10:00", "UTC", "2")) // own title
	h.Post(t, occurrence(1, at(17), ""), timed("Walk", "2026-03-17T11:00", "2026-03-17T12:00", "UTC", "3"))      // own time and Member

	all := timed("Hike", "2026-03-18T09:00", "2026-03-18T10:00", "UTC", "1")
	all.Set("scope", "all")
	if code, body := h.Post(t, occurrence(1, at(18), ""), all); code != http.StatusOK {
		t.Fatalf("All events edit = %d %q", code, body)
	}
	_, page := h.Get(t, "/calendar?view=week&date=2026-03-15")
	for d, want := range map[string]string{"2026-03-15": "09:00–10:00 Hike", "2026-03-16": "09:00–10:00 Long walk", "2026-03-17": "11:00–12:00 Hike", "2026-03-20": "09:00–10:00 Hike"} {
		if !strings.Contains(day(t, page, d), want) {
			t.Errorf("%s = %q, want %q", d, day(t, page, d), want)
		}
	}
	// Ada's filter: the past and the title-only edit follow the new Members; Carol keeps the 17th.
	_, page = h.Get(t, "/calendar?view=week&date=2026-03-15&users=1")
	if !strings.Contains(day(t, page, "2026-03-15"), "Hike") || !strings.Contains(day(t, page, "2026-03-16"), "Long walk") || strings.Contains(day(t, page, "2026-03-17"), "Hike") {
		t.Errorf("Members did not follow All events: %q", page)
	}

	moved := timed("Hike", "2026-03-18T10:00", "2026-03-18T11:00", "UTC", "1")
	moved.Set("scope", "all")
	if code, _ := h.Post(t, occurrence(1, at(18), ""), moved); code != http.StatusBadRequest {
		t.Errorf("All events time change = %d, want 400", code)
	}
	weekly := repeating(timed("Hike", "2026-03-18T09:00", "2026-03-18T10:00", "UTC", "1"), "weekly")
	if code, _ := h.Post(t, occurrence(1, at(18), ""), weekly); code != http.StatusBadRequest {
		t.Errorf("This event repeat change = %d, want 400", code)
	}
}

func TestAllEventsFromAOneOffEditAppliesOnlyWhatChanged(t *testing.T) {
	h := household(t)
	addEvent(t, h, repeating(timed("Walk", "2026-03-15T09:00", "2026-03-15T10:00", "UTC", "2"), "daily"))
	at := func(d int) time.Time { return time.Date(2026, 3, d, 9, 0, 0, 0, time.UTC) }
	h.Post(t, occurrence(1, at(16), ""), timed("Long walk", "2026-03-16T09:00", "2026-03-16T10:00", "UTC", "3"))
	f := timed("Long walk", "2026-03-16T09:00", "2026-03-16T10:00", "UTC", "3")
	f.Set("location", "The park")
	f.Set("scope", "all")
	if code, body := h.Post(t, occurrence(1, at(16), ""), f); code != http.StatusOK {
		t.Fatalf("All events edit = %d %q", code, body)
	}
	_, page := h.Get(t, "/calendar?view=week&date=2026-03-15&users=2")
	if !strings.Contains(day(t, page, "2026-03-18"), "09:00–10:00 Walk") || strings.Contains(page, "Long walk") {
		t.Errorf("the One-off edit's own title and Members spread to the Series: %q", page)
	}
	for _, d := range []int{15, 16} {
		if _, body := h.Get(t, occurrence(1, at(d), "")); !strings.Contains(body, `value="The park"`) {
			t.Errorf("the %dth lacks the new location", d)
		}
	}
}

func TestSplitKeepsCountAndRDatesOnTheirOwnHalf(t *testing.T) {
	h := household(t)
	// Five days from the 15th, plus an extra at 18:00 on the 16th, before the Split.
	addEvent(t, h, repeating(timed("Walk", "2026-03-15T09:00", "2026-03-15T10:00", "UTC"), "custom", "rrule", "RRULE:FREQ=DAILY;COUNT=5\nRDATE:20260316T180000"))
	at := func(d int) time.Time { return time.Date(2026, 3, d, 9, 0, 0, 0, time.UTC) }
	f := repeating(timed("Run", "2026-03-17T09:00", "2026-03-17T10:00", "UTC"), "custom", "rrule", "RRULE:FREQ=DAILY;COUNT=5\r\nRDATE:20260316T180000")
	f.Set("scope", "following")
	if code, body := h.Post(t, occurrence(1, at(17), ""), f); code != http.StatusOK {
		t.Fatalf("Split = %d %q", code, body)
	}
	_, page := h.Get(t, "/calendar?view=week&date=2026-03-15")
	if d := day(t, page, "2026-03-16"); strings.Count(d, "<li>") != 2 || strings.Contains(d, "Run") {
		t.Errorf("16th = %q, want the old half's two Walks", d)
	}
	for d, want := range map[string]bool{"2026-03-19": true, "2026-03-20": false} {
		if got := strings.Contains(day(t, page, d), "Run"); got != want {
			t.Errorf("%s shows Run = %v, want %v (COUNT counts the whole Series)", d, got, want)
		}
	}
}

func TestDeletedFirstOccurrencesDontCountAsBefore(t *testing.T) {
	h := household(t)
	addEvent(t, h, repeating(timed("Walk", "2026-03-15T09:00", "2026-03-15T10:00", "UTC"), "daily"))
	at := func(d int) time.Time { return time.Date(2026, 3, d, 9, 0, 0, 0, time.UTC) }
	h.Post(t, occurrence(1, at(15), "/delete"), nil)
	h.Post(t, occurrence(1, at(16), "/delete"), url.Values{"scope": {"following"}})
	var n int
	h.App.DB.QueryRow("SELECT count(*) FROM events").Scan(&n)
	if n != 0 {
		t.Errorf("%d event rows left, want the Series deleted", n)
	}
}

func TestSplitCountsOccurrencesBeyondTheDisplayCap(t *testing.T) {
	h := household(t)
	addEvent(t, h, repeating(timed("Pill", "2026-01-01T09:00", "2026-01-01T10:00", "UTC"), "custom", "rrule", "RRULE:FREQ=DAILY;COUNT=2000"))
	split := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC).AddDate(0, 0, 1100) // the 1101st
	d := split.Format("2006-01-02")
	f := repeating(timed("Pill", d+"T10:00", d+"T11:00", "UTC"), "custom", "rrule", "RRULE:FREQ=DAILY;COUNT=2000")
	f.Set("scope", "following")
	if code, body := h.Post(t, occurrence(1, split, ""), f); code != http.StatusOK {
		t.Fatalf("Split = %d %q", code, body)
	}
	var rule string
	h.App.DB.QueryRow("SELECT rrule FROM events WHERE id = 2").Scan(&rule)
	if !strings.Contains(rule, "COUNT=900") {
		t.Fatalf("new Series rule = %q, want COUNT=900", rule)
	}
}

func TestEndingASeriesAtAnRDateKeepsItsEarlierEnd(t *testing.T) {
	h := household(t)
	// 1–3 March, plus an extra on the 10th.
	addEvent(t, h, repeating(timed("Walk", "2026-03-01T09:00", "2026-03-01T10:00", "UTC"), "custom", "rrule", "RRULE:FREQ=DAILY;COUNT=3\nRDATE:20260310T090000"))
	if code, _ := h.Post(t, occurrence(1, time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC), "/delete"), url.Values{"scope": {"following"}}); code != http.StatusOK {
		t.Fatalf("This and following delete = %d", code)
	}
	_, page := h.Get(t, "/calendar?view=month&date=2026-03-01")
	for i := 1; i <= 10; i++ {
		d := fmt.Sprintf("2026-03-%02d", i)
		if got := strings.Contains(day(t, page, d), "Walk"); got != (i <= 3) {
			t.Errorf("%s shows Walk = %v, want %v", d, got, i <= 3)
		}
	}
}

func TestSplitToAllDayKeepsExDatesAndRDates(t *testing.T) {
	h := household(t)
	// Sundays from the 15th, plus Wednesday the 25th, but not Sunday the 29th.
	text := "RRULE:FREQ=WEEKLY\nEXDATE:20260329T090000\nRDATE:20260325T090000"
	addEvent(t, h, repeating(timed("Market", "2026-03-15T09:00", "2026-03-15T10:00", "UTC"), "custom", "rrule", text))
	f := repeating(allDay("Market", "2026-03-22", "2026-03-22"), "custom", "rrule", text)
	f.Set("scope", "following")
	if code, body := h.Post(t, occurrence(1, time.Date(2026, 3, 22, 9, 0, 0, 0, time.UTC), ""), f); code != http.StatusOK {
		t.Fatalf("Split = %d %q", code, body)
	}
	_, page := h.Get(t, "/calendar?view=month&date=2026-03-15")
	for d, want := range map[string]bool{"2026-03-22": true, "2026-03-25": true, "2026-03-29": false} {
		if got := strings.Contains(day(t, page, d), "All day Market"); got != want {
			t.Errorf("%s shows the all-day Market = %v, want %v", d, got, want)
		}
	}
}
