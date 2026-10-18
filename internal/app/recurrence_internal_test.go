package app

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	z, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return z
}

// timedMaster is a Series' first Occurrence, given as wall times in tz.
func timedMaster(t *testing.T, tz, start, end string) (calendarEvent, *time.Location) {
	t.Helper()
	z := zone(t, tz)
	t0, err0 := parseLocal(start, z)
	t1, err1 := parseLocal(end, z)
	if err0 != nil || err1 != nil {
		t.Fatal(err0, err1)
	}
	return calendarEvent{id: 7, title: "Series", start: t0.Unix(), end: t1.Unix()}, z
}

func window(first, last string) (time.Time, time.Time) {
	f, _ := time.Parse(dateLayout, first)
	l, _ := time.Parse(dateLayout, last)
	return f, l.AddDate(0, 0, 1)
}

// golden compares one line per Occurrence with testdata/recurrence/name.golden.
func golden(t *testing.T, name string, evs []calendarEvent, z *time.Location) {
	t.Helper()
	var b strings.Builder
	for _, e := range evs {
		if e.allDay {
			fmt.Fprintf(&b, "%s..%s", e.date, e.endDate)
		} else {
			const f = "2006-01-02T15:04Z07:00"
			fmt.Fprintf(&b, "%s..%s", time.Unix(e.start, 0).In(z).Format(f), time.Unix(e.end, 0).In(z).Format(f))
		}
		fmt.Fprintf(&b, " %s original=%d\n", e.title, e.original)
	}
	path := filepath.Join("testdata", "recurrence", name+".golden")
	if *update {
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != strings.ReplaceAll(string(want), "\r\n", "\n") {
		t.Errorf("%s:\ngot\n%swant\n%s", name, got, want)
	}
}

func TestExpandTimedSeries(t *testing.T) {
	for _, c := range []struct {
		name, tz, start, end string
		s                    series
		from, to             string // window, last day inclusive
	}{
		{"interval", "UTC", "2026-03-02T09:00", "2026-03-02T10:00", series{rule: "FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE"}, "2026-03-01", "2026-03-31"},
		{"second-tuesday", "Europe/London", "2026-01-13T18:00", "2026-01-13T19:00", series{rule: "FREQ=MONTHLY;BYDAY=2TU"}, "2026-01-01", "2026-06-30"},
		{"last-friday", "UTC", "2026-01-30T17:00", "2026-01-30T18:00", series{rule: "FREQ=MONTHLY;BYDAY=-1FR"}, "2026-01-01", "2026-06-30"},
		{"count", "UTC", "2026-03-10T09:00", "2026-03-10T10:00", series{rule: "FREQ=DAILY;COUNT=3"}, "2026-03-01", "2026-03-31"},
		// UNTIL is a UTC instant: 16:00Z is 09:00 in Los Angeles on the 12th, so the 12th is the last.
		{"until", "America/Los_Angeles", "2026-03-10T09:00", "2026-03-10T10:00", series{rule: "FREQ=DAILY;UNTIL=20260312T160000Z"}, "2026-03-01", "2026-03-31"},
		{"exdate-rdate", "UTC", "2026-03-02T09:00", "2026-03-02T10:00", series{rule: "FREQ=WEEKLY", exdates: []string{"2026-03-09T09:00"}, rdates: []string{"2026-03-11T14:00"}}, "2026-03-01", "2026-03-31"},
		// Los Angeles springs forward on 8 March 2026 and falls back on 1 November.
		{"dst-spring", "America/Los_Angeles", "2026-03-01T09:00", "2026-03-01T10:00", series{rule: "FREQ=WEEKLY"}, "2026-03-01", "2026-03-21"},
		{"dst-fall", "America/Los_Angeles", "2026-10-25T09:00", "2026-10-25T10:00", series{rule: "FREQ=WEEKLY"}, "2026-10-25", "2026-11-14"},
		{"gap-0230", "America/Los_Angeles", "2026-03-06T02:30", "2026-03-06T03:00", series{rule: "FREQ=DAILY"}, "2026-03-06", "2026-03-10"},
		{"overlap-0130", "America/Los_Angeles", "2026-10-30T01:30", "2026-10-30T02:00", series{rule: "FREQ=DAILY"}, "2026-10-30", "2026-11-02"},
		// RFC 5545: a nonexistent time isn't counted, so 8 March doesn't use up a COUNT slot.
		{"count-gap", "America/Los_Angeles", "2026-03-06T02:30", "2026-03-06T03:00", series{rule: "FREQ=DAILY;COUNT=3"}, "2026-03-06", "2026-03-12"},
		// 09:15Z on 1 November is the second 01:15 there; the first 01:30 (08:30Z) is before it.
		{"until-fallback", "America/Los_Angeles", "2026-10-30T01:30", "2026-10-30T02:00", series{rule: "FREQ=DAILY;UNTIL=20261101T091500Z"}, "2026-10-30", "2026-11-03"},
		{"yearly", "Europe/London", "2026-07-04T15:00", "2026-07-04T17:00", series{rule: "FREQ=YEARLY"}, "2026-01-01", "2028-12-31"},
		{"before-window", "UTC", "2020-01-01T23:00", "2020-01-02T01:00", series{rule: "FREQ=DAILY"}, "2026-03-15", "2026-03-15"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, z := timedMaster(t, c.tz, c.start, c.end)
			from, to := window(c.from, c.to)
			evs, err := expand(m, c.s, z, nil, from, to)
			if err != nil {
				t.Fatal(err)
			}
			golden(t, c.name, evs, z)
		})
	}
}

func TestExpandAllDaySeries(t *testing.T) {
	m := calendarEvent{id: 7, title: "Leap day", allDay: true, date: "2024-02-29", endDate: "2024-03-01"}
	from, to := window("2026-01-01", "2029-12-31")
	evs, err := expand(m, series{rule: "FREQ=YEARLY"}, nil, nil, from, to)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "leap-day", evs, time.UTC)
}

func TestExpandMergesOneOffEdits(t *testing.T) {
	m, z := timedMaster(t, "UTC", "2026-03-10T09:00", "2026-03-10T10:00")
	day := func(d int) int64 { return time.Date(2026, 3, d, 9, 0, 0, 0, time.UTC).Unix() }
	moved := calendarEvent{id: 7, title: "Moved", start: time.Date(2026, 3, 11, 15, 0, 0, 0, time.UTC).Unix(), end: time.Date(2026, 3, 11, 16, 0, 0, 0, time.UTC).Unix()}
	away := calendarEvent{id: 7, title: "Moved out", start: time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC).Unix(), end: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC).Unix()}
	from, to := window("2026-03-10", "2026-03-14")
	evs, err := expand(m, series{rule: "FREQ=DAILY"}, z, []oneOff{
		{original: day(11), ev: moved},
		{original: day(12), cancelled: true},
		{original: day(13), ev: away}, // moved out of the window: neither the original nor the edit shows
	}, from, to)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "one-off-edits", evs, z)
}

func TestExpandStopsAtTheCap(t *testing.T) {
	m, z := timedMaster(t, "UTC", "2026-01-01T09:00", "2026-01-01T10:00")
	from, to := window("2026-01-01", "2029-12-31")
	for _, c := range []struct {
		rule string
		want int
	}{
		{"FREQ=DAILY;COUNT=999", 999},
		{"FREQ=DAILY;COUNT=1000", 1000},
		{"FREQ=DAILY;COUNT=1001", maxOccurrences},
	} {
		evs, err := expand(m, series{rule: c.rule}, z, nil, from, to)
		if err != nil || len(evs) != c.want {
			t.Errorf("%s: %d occurrences, %v; want %d", c.rule, len(evs), err, c.want)
		}
	}
}

func TestExpandCapCountsOneOffEdits(t *testing.T) {
	m, z := timedMaster(t, "UTC", "2026-01-01T09:00", "2026-01-01T10:00")
	var edits []oneOff
	for i := range maxOccurrences + 1 {
		t0 := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC).AddDate(0, 0, i)
		edits = append(edits, oneOff{original: t0.Unix(), ev: calendarEvent{title: "Edited", start: t0.Add(time.Hour).Unix(), end: t0.Add(2 * time.Hour).Unix()}})
	}
	from, to := window("2026-01-01", "2029-12-31")
	evs, err := expand(m, series{rule: "FREQ=DAILY"}, z, edits, from, to)
	if err != nil || len(evs) != maxOccurrences {
		t.Fatalf("%d occurrences, %v; want %d", len(evs), err, maxOccurrences)
	}
}

func TestEndBeforeAddsUntilAndDropsLaterRDates(t *testing.T) {
	london := zone(t, "Europe/London")
	o := time.Date(2026, 6, 10, 9, 0, 0, 0, london).Unix() // 08:00 UTC
	got := endBefore(series{rule: "FREQ=DAILY;COUNT=10;INTERVAL=1", rdates: []string{"2026-06-09T09:00", "2026-06-10T09:00", "2026-06-12T09:00"}}, london, o)
	// COUNT stays: it ends the Series sooner if it already did.
	if want := "FREQ=DAILY;COUNT=10;INTERVAL=1;UNTIL=20260610T075959Z"; got.rule != want {
		t.Errorf("rule = %q, want %q", got.rule, want)
	}
	if len(got.rdates) != 1 || got.rdates[0] != "2026-06-09T09:00" {
		t.Errorf("rdates = %v, want only the one before the Split", got.rdates)
	}
	day := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC).Unix()
	if got := endBefore(series{rule: "FREQ=WEEKLY;UNTIL=20270101T000000Z", rdates: []string{"2026-06-10"}}, nil, day); got.rule != "FREQ=WEEKLY;UNTIL=20260609T235959Z" || len(got.rdates) != 0 {
		t.Errorf("all-day = %+v", got)
	}
}

func TestEndBeforeKeepsAnEarlierUntil(t *testing.T) {
	o := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC).Unix()
	if got := endBefore(series{rule: "FREQ=DAILY;UNTIL=20260303T090000Z"}, time.UTC, o); got.rule != "FREQ=DAILY;UNTIL=20260303T090000Z" {
		t.Errorf("rule = %q, want its own earlier UNTIL", got.rule)
	}
}

func TestRetypeMovesStoredDatesBetweenKinds(t *testing.T) {
	if got := retype([]string{"2026-03-25T09:00"}, true, ""); got[0] != "2026-03-25" {
		t.Errorf("to all-day = %v", got)
	}
	if got := retype([]string{"2026-03-25"}, false, "18:30"); got[0] != "2026-03-25T18:30" {
		t.Errorf("to timed = %v", got)
	}
}
