package app_test

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-ical"
)

// getICS fetches path and decodes it with go-ical, as a subscribing client would.
func getICS(t *testing.T, h interface {
	Get(testing.TB, string) (int, string)
}, path string) *ical.Calendar {
	t.Helper()
	code, body := h.Get(t, path)
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d %q", path, code, body)
	}
	cal, err := ical.NewDecoder(strings.NewReader(body)).Decode()
	if err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return cal
}

func TestICSExport(t *testing.T) {
	h := household(t)
	london, _ := time.LoadLocation("Europe/London")
	at := func(d, hour int) time.Time { return time.Date(2026, 3, d, hour, 0, 0, 0, london) }
	// Mondays at 09:00 London across the 29 March clock change; a wall-time UNTIL and an EXDATE.
	addEvent(t, h, repeating(timed("Swim", "2026-03-16T09:00", "2026-03-16T10:00", "Europe/London"), "custom",
		"rrule", "RRULE:FREQ=WEEKLY;BYDAY=MO;UNTIL=20260414\nEXDATE:20260323T090000"))
	moved := timed("Late swim", "2026-03-30T10:00", "2026-03-30T11:00", "Europe/London")
	if code, body := h.Post(t, occurrence(1, at(30, 9), ""), moved); code != http.StatusOK {
		t.Fatalf("This event edit = %d %q", code, body)
	}
	if code, _ := h.Post(t, occurrence(1, at(37, 9), "/delete"), nil); code != http.StatusOK { // 6 April
		t.Fatalf("delete Occurrence = %d", code)
	}
	addEvent(t, h, repeating(allDay("Bins", "2026-03-15", "2026-03-15"), "daily", "interval", "3"))
	h.Post(t, "/calendar/calendars", url.Values{"name": {"Work"}, "color": {"#000000"}})
	work := timed("Standup", "2026-03-16T09:00", "2026-03-16T09:15", "UTC")
	work.Set("calendar_id", "2")
	addEvent(t, h, work)

	if _, page := h.Get(t, "/calendar"); !strings.Contains(page, `href="http://gocasa.test/calendar.ics?calendars=1"`) {
		t.Fatal("calendar page has no absolute ICS link")
	}

	cal := getICS(t, h, "/calendar.ics?calendars=1")
	byTitle := map[string]ical.Event{}
	for _, e := range cal.Events() {
		title, _ := e.Props.Text(ical.PropSummary)
		byTitle[title] = e
	}
	if _, ok := byTitle["Standup"]; ok || len(byTitle) != 3 {
		t.Fatalf("exported %v, want Swim, its One-off edit and Bins only", byTitle)
	}

	swim := byTitle["Swim"]
	set, err := swim.RecurrenceSet(nil)
	if err != nil || set == nil {
		t.Fatalf("Swim recurrence = %v, %v", set, err)
	}
	// 23 March is an EXDATE, 6 April is deleted; UNTIL is 14 April's local midnight.
	want := []time.Time{at(16, 9), at(30, 9), at(44, 9)}
	if got := set.Between(at(1, 0), at(60, 0), true); !slices.EqualFunc(got, want, time.Time.Equal) {
		t.Errorf("Swim Occurrences = %v, want %v", got, want)
	}
	edit := byTitle["Late swim"]
	uid, _ := swim.Props.Text(ical.PropUID)
	editUID, _ := edit.Props.Text(ical.PropUID)
	rid, err1 := edit.Props.DateTime(ical.PropRecurrenceID, nil)
	start, err2 := edit.DateTimeStart(nil)
	if editUID != uid || err1 != nil || !rid.Equal(at(30, 9)) || err2 != nil || !start.Equal(at(30, 10)) {
		t.Errorf("One-off edit UID %q (Series %q), RECURRENCE-ID %v %v, DTSTART %v %v", editUID, uid, rid, err1, start, err2)
	}
	if tzid := edit.Props.Get(ical.PropDateTimeStart).Params.Get(ical.PropTimezoneID); tzid != "Europe/London" {
		t.Errorf("DTSTART TZID = %q", tzid)
	}

	bins := byTitle["Bins"]
	dtstart, dtend := bins.Props.Get(ical.PropDateTimeStart), bins.Props.Get(ical.PropDateTimeEnd)
	if dtstart.ValueType() != ical.ValueDate || dtstart.Value != "20260315" || dtend.Value != "20260316" {
		t.Errorf("Bins DTSTART %v DTEND %v, want dates", dtstart, dtend)
	}

	if all := getICS(t, h, "/calendar.ics"); len(all.Events()) != 4 {
		t.Errorf("all calendars exported %d events, want 4", len(all.Events()))
	}
	if empty := getICS(t, h, "/calendar.ics?calendars=99"); len(empty.Events()) != 0 {
		t.Errorf("unknown calendar exported %d events", len(empty.Events()))
	}
}
