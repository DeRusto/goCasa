package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/config"
)

// household is an app with Ada (Admin, 1), Bob (2), Carol (3) and local calendar 1.
func household(t *testing.T) *App {
	t.Helper()
	a, err := New(context.Background(), Options{
		Config: config.Config{
			BaseURL:       &url.URL{Scheme: "http", Host: "gocasa.test"},
			DataDir:       t.TempDir(),
			Location:      time.UTC,
			EncryptionKey: make([]byte, 32),
			SessionSecret: []byte(strings.Repeat("s", 32)),
		},
		Now:    func() time.Time { return time.Date(2026, 3, 15, 9, 0, 0, 0, time.UTC) },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	if _, err := a.DB.Exec(`INSERT INTO members (name, color, role, pin_hash, created_at) VALUES
			('Ada', '#3b82f6', 'admin', x'00', 0), ('Bob', '#22c55e', 'member', NULL, 0), ('Carol', '#ef4444', 'member', NULL, 0);
		INSERT INTO source_calendars (integration_id, name, color) VALUES (1, 'Family', '#a855f7')`); err != nil {
		t.Fatal(err)
	}
	return a
}

func at(day, hour int) time.Time { return time.Date(2026, 3, day, hour, 0, 0, 0, time.UTC) }

func key(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

func timedAt(title string, start time.Time, hours int, members ...int64) eventChange {
	return eventChange{calendarID: 1, title: title, start: start.Unix(), end: start.Add(time.Duration(hours) * time.Hour).Unix(), tzid: "UTC",
		members: members, exdates: "[]", rdates: "[]"}
}

func allDayOn(title, date string) eventChange {
	d, _ := time.Parse(dateLayout, date)
	return eventChange{calendarID: 1, title: title, allDay: true, date: date, endDate: d.AddDate(0, 0, 1).Format(dateLayout), exdates: "[]", rdates: "[]"}
}

// repeat is c with a new rule, EXDATEs and RDATEs (as stored).
func repeat(c eventChange, rule string, exdates, rdates []string) eventChange {
	ex, _ := json.Marshal(append([]string{}, exdates...))
	rd, _ := json.Marshal(append([]string{}, rdates...))
	c.rrule, c.exdates, c.rdates, c.rerule = rule, string(ex), string(rd), true
	return c
}

func write(t *testing.T, a *App, id int64, occurrence string, sc scope, c eventChange) int64 {
	t.Helper()
	id, err := a.writeEvent(context.Background(), id, occurrence, sc, c)
	if err != nil {
		t.Fatalf("writeEvent: %v", err)
	}
	return id
}

func remove(t *testing.T, a *App, id int64, occurrence string, sc scope) {
	t.Helper()
	if err := a.removeEvent(context.Background(), id, occurrence, sc); err != nil {
		t.Fatalf("removeEvent: %v", err)
	}
}

// agenda is each event touching first..last (UTC dates), sorted, as
// "MM-DD HH:MM–HH:MM title" or "MM-DD all day title".
func agenda(t *testing.T, a *App, first, last string, users ...int64) []string {
	t.Helper()
	from, _ := time.Parse(dateLayout, first)
	to, _ := time.Parse(dateLayout, last)
	evs, err := a.eventsIn(context.Background(), from, to.AddDate(0, 0, 1), users)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range evs {
		if e.allDay {
			out = append(out, e.date[5:]+" all day "+e.title)
		} else {
			out = append(out, time.Unix(e.start, 0).UTC().Format("01-02 15:04")+"–"+time.Unix(e.end, 0).UTC().Format("15:04")+" "+e.title)
		}
	}
	slices.Sort(out)
	return out
}

func wantAgenda(t *testing.T, got []string, want ...string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("agenda =\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func count(t *testing.T, a *App, query string, args ...any) int {
	t.Helper()
	var n int
	if err := a.DB.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func refused(t *testing.T, err error) *refusal {
	t.Helper()
	var r *refusal
	if !errors.As(err, &r) {
		t.Fatalf("err = %v, want a refusal", err)
	}
	return r
}

func TestThisEventEditsAndDeletesOneOccurrence(t *testing.T) {
	a := household(t)
	changes := a.Bus.subscribe()
	defer a.Bus.unsubscribe(changes)
	id := write(t, a, 0, "", scopeThis, repeat(timedAt("Walk", at(15, 9), 1, 2), "FREQ=DAILY", nil, nil))
	if c := <-changes; c != (Change{Type: "created", Entity: "event", ID: id}) {
		t.Errorf("published %+v", c)
	}
	write(t, a, id, key(at(16, 9)), scopeThis, timedAt("Long walk", at(16, 11), 2, 3))
	remove(t, a, id, key(at(17, 9)), scopeThis)
	wantAgenda(t, agenda(t, a, "2026-03-15", "2026-03-18"), "03-15 09:00–10:00 Walk", "03-16 11:00–13:00 Long walk", "03-18 09:00–10:00 Walk")
	// Members follow the One-off edit.
	wantAgenda(t, agenda(t, a, "2026-03-15", "2026-03-18", 3), "03-16 11:00–13:00 Long walk")

	// Editing it again updates the same One-off edit row.
	write(t, a, id, key(at(16, 9)), scopeThis, timedAt("Longer walk", at(16, 11), 3))
	if n := count(t, a, "SELECT count(*) FROM events WHERE recurring_master_id = ? AND original_start_utc = ?", id, at(16, 9).Unix()); n != 1 {
		t.Fatalf("%d One-off edit rows for one Occurrence, want 1", n)
	}

	// A time the Series never has is not an Occurrence; neither is one before it; an edit row is not an event of its own.
	if _, err := a.findOccurrence(context.Background(), id, key(at(16, 10))); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("made-up Occurrence: %v", err)
	}
	if err := a.removeEvent(context.Background(), id, key(at(14, 9)), scopeThis); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Occurrence before the Series: %v", err)
	}
	if _, err := a.findOccurrence(context.Background(), id+1, ""); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("One-off edit row: %v", err)
	}
	// A repeat change needs This and following.
	if _, err := a.writeEvent(context.Background(), id, key(at(18, 9)), scopeThis, repeat(timedAt("Walk", at(18, 9), 1), "FREQ=WEEKLY", nil, nil)); refused(t, err).conflict {
		t.Error("a repeat change under This event is a conflict, want refused")
	}
}

func TestSplitResetsLaterOneOffEditsOnceConfirmed(t *testing.T) {
	a := household(t)
	id := write(t, a, 0, "", scopeThis, repeat(timedAt("Walk", at(15, 9), 1, 2), "FREQ=DAILY", nil, nil))
	write(t, a, id, key(at(16, 9)), scopeThis, timedAt("Early walk", at(16, 6), 1)) // before the Split: kept
	write(t, a, id, key(at(18, 9)), scopeThis, timedAt("Late walk", at(18, 18), 1))
	remove(t, a, id, key(at(20, 9)), scopeThis)

	run := repeat(timedAt("Run", at(17, 7), 1, 3), "FREQ=DAILY", nil, nil)
	_, err := a.writeEvent(context.Background(), id, key(at(17, 9)), scopeFollowing, run)
	if r := refused(t, err); !r.conflict || r.reset != 2 {
		t.Fatalf("unconfirmed Split = %+v, want a conflict over 2 One-off edits", r)
	}
	run.confirmReset = 2
	newID := write(t, a, id, key(at(17, 9)), scopeFollowing, run)
	wantAgenda(t, agenda(t, a, "2026-03-15", "2026-03-20"), "03-15 09:00–10:00 Walk", "03-16 06:00–07:00 Early walk",
		"03-17 07:00–08:00 Run", "03-18 07:00–08:00 Run", "03-19 07:00–08:00 Run", "03-20 07:00–08:00 Run")
	// The halves aren't linked: the new Series is its own event and the old one ends.
	if newID == id {
		t.Error("the new Series reuses the old one's id")
	}
	if n := count(t, a, "SELECT count(*) FROM events WHERE id = ? AND rrule LIKE '%UNTIL=20260317T085959Z%'", id); n != 1 {
		t.Error("old Series does not end before the Split")
	}
}

func TestSplitOnTheFirstOccurrenceCoversEverything(t *testing.T) {
	a := household(t)
	id := write(t, a, 0, "", scopeThis, repeat(allDayOn("Bins", "2026-03-15"), "FREQ=WEEKLY", nil, nil))
	first := key(time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC))
	write(t, a, id, first, scopeFollowing, repeat(allDayOn("Recycling", "2026-03-16"), "FREQ=WEEKLY", nil, nil))
	wantAgenda(t, agenda(t, a, "2026-03-15", "2026-03-23"), "03-16 all day Recycling", "03-23 all day Recycling")
	if n := count(t, a, "SELECT count(*) FROM events"); n != 1 {
		t.Errorf("%d event rows, want the one Series", n)
	}
}

func TestAllEventsEditKeepsOneOffEditsOwnChanges(t *testing.T) {
	a := household(t)
	id := write(t, a, 0, "", scopeThis, repeat(timedAt("Walk", at(15, 9), 1, 2), "FREQ=DAILY", nil, nil))
	write(t, a, id, key(at(16, 9)), scopeThis, timedAt("Long walk", at(16, 9), 1, 2)) // own title
	write(t, a, id, key(at(17, 9)), scopeThis, timedAt("Walk", at(17, 11), 1, 3))     // own time and Member

	write(t, a, id, key(at(18, 9)), scopeAll, timedAt("Hike", at(18, 9), 1, 1))
	wantAgenda(t, agenda(t, a, "2026-03-15", "2026-03-18"), "03-15 09:00–10:00 Hike", "03-16 09:00–10:00 Long walk", "03-17 11:00–12:00 Hike", "03-18 09:00–10:00 Hike")
	// Ada's filter: the Series and the title-only edit follow the new Members; Carol keeps the 17th.
	wantAgenda(t, agenda(t, a, "2026-03-15", "2026-03-17", 1), "03-15 09:00–10:00 Hike", "03-16 09:00–10:00 Long walk")

	if _, err := a.writeEvent(context.Background(), id, key(at(18, 9)), scopeAll, timedAt("Hike", at(18, 10), 1, 1)); refused(t, err).conflict {
		t.Error("an All events time change is a conflict, want refused")
	}
}

func TestAllEventsFromAOneOffEditAppliesOnlyWhatChanged(t *testing.T) {
	a := household(t)
	id := write(t, a, 0, "", scopeThis, repeat(timedAt("Walk", at(15, 9), 1, 2), "FREQ=DAILY", nil, nil))
	write(t, a, id, key(at(16, 9)), scopeThis, timedAt("Long walk", at(16, 9), 1, 3))
	c := timedAt("Long walk", at(16, 9), 1, 3)
	c.location = "The park"
	write(t, a, id, key(at(16, 9)), scopeAll, c)
	wantAgenda(t, agenda(t, a, "2026-03-15", "2026-03-17", 2), "03-15 09:00–10:00 Walk", "03-17 09:00–10:00 Walk")
	if n := count(t, a, "SELECT count(*) FROM events WHERE location = 'The park'"); n != 2 {
		t.Errorf("%d rows have the new location, want the Series and its One-off edit", n)
	}
}

func TestSplitKeepsCountAndRDatesOnTheirOwnHalf(t *testing.T) {
	a := household(t)
	// Five days from the 15th, plus an extra at 18:00 on the 16th, before the Split.
	id := write(t, a, 0, "", scopeThis, repeat(timedAt("Walk", at(15, 9), 1), "FREQ=DAILY;COUNT=5", nil, []string{"2026-03-16T18:00"}))
	write(t, a, id, key(at(17, 9)), scopeFollowing, timedAt("Run", at(17, 9), 1)) // the rule unchanged
	wantAgenda(t, agenda(t, a, "2026-03-15", "2026-03-21"), "03-15 09:00–10:00 Walk", "03-16 09:00–10:00 Walk", "03-16 18:00–19:00 Walk",
		"03-17 09:00–10:00 Run", "03-18 09:00–10:00 Run", "03-19 09:00–10:00 Run")
}

func TestSplitCountsOccurrencesBeyondTheDisplayCap(t *testing.T) {
	a := household(t)
	start := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	id := write(t, a, 0, "", scopeThis, repeat(timedAt("Pill", start, 1), "FREQ=DAILY;COUNT=2000", nil, nil))
	split := start.AddDate(0, 0, 1100) // the 1101st
	newID := write(t, a, id, key(split), scopeFollowing, timedAt("Pill", split.Add(time.Hour), 1))
	if n := count(t, a, "SELECT count(*) FROM events WHERE id = ? AND rrule LIKE '%COUNT=900%'", newID); n != 1 {
		t.Fatal("new Series does not keep the 900 Occurrences left")
	}
}

func TestThisAndFollowingDeleteEndsTheSeries(t *testing.T) {
	a := household(t)
	id := write(t, a, 0, "", scopeThis, repeat(timedAt("Walk", at(15, 9), 1), "FREQ=DAILY", nil, nil))
	write(t, a, id, key(at(19, 9)), scopeThis, timedAt("Late walk", at(19, 18), 1))
	remove(t, a, id, key(at(17, 9)), scopeFollowing)
	wantAgenda(t, agenda(t, a, "2026-03-15", "2026-03-21"), "03-15 09:00–10:00 Walk", "03-16 09:00–10:00 Walk")
	// On the first Occurrence it deletes the whole Series.
	remove(t, a, id, key(at(15, 9)), scopeFollowing)
	if _, err := a.findOccurrence(context.Background(), id, ""); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("Series after deleting from its first Occurrence: %v", err)
	}
}

func TestDeletedFirstOccurrencesDontCountAsBefore(t *testing.T) {
	a := household(t)
	id := write(t, a, 0, "", scopeThis, repeat(timedAt("Walk", at(15, 9), 1), "FREQ=DAILY", nil, nil))
	remove(t, a, id, key(at(15, 9)), scopeThis)
	remove(t, a, id, key(at(16, 9)), scopeFollowing)
	if n := count(t, a, "SELECT count(*) FROM events"); n != 0 {
		t.Errorf("%d event rows left, want the Series deleted", n)
	}
}

func TestAllEventsDeleteRemovesTheSeries(t *testing.T) {
	a := household(t)
	id := write(t, a, 0, "", scopeThis, repeat(allDayOn("Bins", "2026-03-15"), "FREQ=WEEKLY", nil, nil))
	remove(t, a, id, key(time.Date(2026, 3, 22, 0, 0, 0, 0, time.UTC)), scopeAll)
	if n := count(t, a, "SELECT count(*) FROM events"); n != 0 {
		t.Errorf("%d event rows left, want none", n)
	}
}

func TestEndingASeriesAtAnRDateKeepsItsEarlierEnd(t *testing.T) {
	a := household(t)
	// 1–3 March, plus an extra on the 10th.
	id := write(t, a, 0, "", scopeThis, repeat(timedAt("Walk", at(1, 9), 1), "FREQ=DAILY;COUNT=3", nil, []string{"2026-03-10T09:00"}))
	remove(t, a, id, key(at(10, 9)), scopeFollowing)
	wantAgenda(t, agenda(t, a, "2026-03-01", "2026-03-10"), "03-01 09:00–10:00 Walk", "03-02 09:00–10:00 Walk", "03-03 09:00–10:00 Walk")
}

func TestSplitToAllDayKeepsExDatesAndRDates(t *testing.T) {
	a := household(t)
	// Sundays from the 15th, plus Wednesday the 25th, but not Sunday the 29th.
	id := write(t, a, 0, "", scopeThis, repeat(timedAt("Market", at(15, 9), 1), "FREQ=WEEKLY", []string{"2026-03-29T09:00"}, []string{"2026-03-25T09:00"}))
	write(t, a, id, key(at(22, 9)), scopeFollowing, allDayOn("Market", "2026-03-22"))
	wantAgenda(t, agenda(t, a, "2026-03-22", "2026-03-29"), "03-22 all day Market", "03-25 all day Market")
}

func TestWritesOnlyGoToLocalCalendars(t *testing.T) {
	a := household(t)
	c := timedAt("Walk", at(15, 9), 1)
	c.calendarID = 99
	if _, err := a.writeEvent(context.Background(), 0, "", scopeThis, c); refused(t, err).msg != "Pick a calendar." {
		t.Errorf("unknown calendar: %v", err)
	}
}

func TestAllEventsTitleEditAcrossTheFallBackHour(t *testing.T) {
	a := household(t)
	z := zone(t, "America/New_York")
	start, _ := parseLocal("2026-10-31T00:30", z)
	c := timedAt("Walk", start, 2)
	c.tzid = "America/New_York"
	id := write(t, a, 0, "", scopeThis, repeat(c, "FREQ=DAILY", nil, nil))
	// On 1 November it ends at the second 01:30 (EST); the dialog's 01:30 reads back as the first (EDT).
	t0, _ := parseLocal("2026-11-01T00:30", z)
	t1, _ := parseLocal("2026-11-01T01:30", z)
	c = eventChange{calendarID: 1, title: "Run", start: t0.Unix(), end: t1.Unix(), tzid: "America/New_York", exdates: "[]", rdates: "[]"}
	write(t, a, id, key(t0), scopeAll, c)
	if n := count(t, a, "SELECT count(*) FROM events WHERE id = ? AND title = 'Run'", id); n != 1 {
		t.Error("the title change did not apply")
	}
}

func TestMemberFilterAlwaysShowsHouseholdEventsAndColoursFollowMembers(t *testing.T) {
	a := household(t)
	write(t, a, 0, "", scopeThis, timedAt("Dinner", at(15, 18), 1))      // Household
	write(t, a, 0, "", scopeThis, timedAt("Football", at(15, 10), 1, 2)) // Bob
	write(t, a, 0, "", scopeThis, timedAt("Piano", at(15, 12), 1, 3))    // Carol
	write(t, a, 0, "", scopeThis, timedAt("Zoo", at(15, 14), 1, 2, 3))   // both
	wantAgenda(t, agenda(t, a, "2026-03-15", "2026-03-15", 2), "03-15 10:00–11:00 Football", "03-15 14:00–15:00 Zoo", "03-15 18:00–19:00 Dinner")
	wantAgenda(t, agenda(t, a, "2026-03-15", "2026-03-15", 2, 3), "03-15 10:00–11:00 Football", "03-15 12:00–13:00 Piano", "03-15 14:00–15:00 Zoo", "03-15 18:00–19:00 Dinner")

	// Household events take their calendar's colour; others their Members', by name: Abby (3) before Bob (2).
	if _, err := a.DB.Exec("UPDATE members SET name = 'Abby' WHERE id = 3"); err != nil {
		t.Fatal(err)
	}
	evs, err := a.eventsIn(context.Background(), at(15, 0), at(16, 0), nil)
	if err != nil {
		t.Fatal(err)
	}
	colors := map[string]string{}
	for _, e := range evs {
		colors[e.title] = strings.Join(e.colors, ",")
	}
	want := map[string]string{"Dinner": "#a855f7", "Football": "#22c55e", "Piano": "#ef4444", "Zoo": "#ef4444,#22c55e"}
	for title, c := range want {
		if colors[title] != c {
			t.Errorf("%s colours = %q, want %q", title, colors[title], c)
		}
	}
}

func TestOccurrenceMovedBeforeItsSeriesStillShows(t *testing.T) {
	a := household(t)
	first := time.Date(2026, 12, 7, 19, 0, 0, 0, time.UTC)
	id := write(t, a, 0, "", scopeThis, repeat(timedAt("Choir", first, 1), "FREQ=WEEKLY", nil, nil))
	write(t, a, id, key(first), scopeThis, timedAt("Choir (early)", time.Date(2026, 10, 20, 19, 0, 0, 0, time.UTC), 1))
	wantAgenda(t, agenda(t, a, "2026-10-20", "2026-10-20"), "10-20 19:00–20:00 Choir (early)")
}

func TestAllDayEventsCoverTheirDatesInclusive(t *testing.T) {
	a := household(t)
	c := allDayOn("Camping", "2026-03-14")
	c.endDate = "2026-03-17" // the 14th to the 16th
	write(t, a, 0, "", scopeThis, c)
	for _, w := range []struct{ first, last string }{{"2026-03-14", "2026-03-14"}, {"2026-03-16", "2026-03-21"}} {
		wantAgenda(t, agenda(t, a, w.first, w.last), "03-14 all day Camping")
	}
	wantAgenda(t, agenda(t, a, "2026-03-17", "2026-03-21"))
}
