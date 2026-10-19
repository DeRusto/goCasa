package app_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/DeRusto/goCasa/internal/apptest"
)

// withEtag is e with Google's etag.
func withEtag(e map[string]any, etag string) map[string]any { e["etag"] = etag; return e }

// googleEdit is the event dialog's fields for a timed event in Europe/London.
func googleEdit(title, start, end string, extra ...string) url.Values {
	v := url.Values{"title": {title}, "start": {start}, "end": {end}, "tz": {"Europe/London"}}
	for i := 0; i+1 < len(extra); i += 2 {
		v.Add(extra[i], extra[i+1])
	}
	return v
}

// submit opens Google event path's dialog and posts v to path+action with
// the version it shows, as a browser would.
func submit(t *testing.T, h *apptest.Harness, path, action string, v url.Values) (int, string) {
	t.Helper()
	_, page := h.Get(t, path)
	_, rest, _ := strings.Cut(page, `name="etag" value="`)
	etag, _, _ := strings.Cut(rest, `"`)
	if v == nil {
		v = url.Values{}
	}
	v.Set("etag", etag)
	return h.Post(t, path+action, v)
}

// syncedWith is syncedGoogle with events in Alice's calendar, synced.
func syncedWith(t *testing.T, g *fakeGoogle, events ...map[string]any) (*apptest.Harness, int64) {
	t.Helper()
	g.events["alice@example.com"] = events
	h, cal := syncedGoogle(t, g)
	h.RunDue()
	return h, cal
}

// An edit goes to Google first and lands through the sync straight after; when
// Google can't be reached, nothing changes.
func TestGoogleEditWritesFirst(t *testing.T) {
	g := newGoogle(t)
	h, _ := syncedWith(t, g, withEtag(gTimed("dentist", "Dentist", "2026-03-16T10:00:00Z", "2026-03-16T11:00:00Z"), "e1"))
	dentist := eventID(t, h, "dentist")
	path := fmt.Sprintf("/calendar/synced/%d", dentist)
	if _, page := h.Get(t, path); !strings.Contains(page, `value="Dentist"`) || !strings.Contains(page, "Saving…") {
		t.Fatalf("a Google event's dialog:\n%s", page)
	}

	// Unreachable: an error, no sync, no change, Members included.
	g.set(func(g *fakeGoogle) { g.writeFail = http.StatusServiceUnavailable })
	n := len(g.requests())
	code, page := submit(t, h, path, "/edit", googleEdit("Dentist (moved)", "2026-03-16T14:00", "2026-03-16T15:00", "members", "1"))
	if code != http.StatusBadGateway || !strings.Contains(page, "reach Google, so nothing changed") {
		t.Fatalf("an edit Google didn't take = %d:\n%s", code, page)
	}
	if d := day(t, agendaPage(t, h), "2026-03-16"); !strings.Contains(d, "10:00–11:00 Dentist") || !strings.Contains(d, "#22c55e") || len(g.requests()) != n {
		t.Fatalf("a failed edit changed the dentist: %q", d)
	}

	g.set(func(g *fakeGoogle) {
		g.writeFail = 0
		g.changes["alice@example.com"] = []map[string]any{withEtag(gTimed("dentist", "Dentist (moved)", "2026-03-16T14:00:00Z", "2026-03-16T15:00:00Z"), "e2")}
	})
	if code, _ := submit(t, h, path, "/edit", googleEdit("Dentist (moved)", "2026-03-16T14:00", "2026-03-16T15:00", "members", "1", "location", "Clinic")); code != http.StatusOK {
		t.Fatalf("editing = %d", code)
	}
	w := g.sent()[len(g.sent())-1]
	start, _ := w.body["start"].(map[string]any)
	if w.method != http.MethodPatch || w.id != "dentist" || w.ifMatch != "e1" || w.body["summary"] != "Dentist (moved)" || w.body["location"] != "Clinic" ||
		start["dateTime"] != "2026-03-16T14:00:00Z" || start["timeZone"] != "Europe/London" {
		t.Fatalf("Google got %+v", w)
	}
	if d := day(t, agendaPage(t, h), "2026-03-16"); !strings.Contains(d, "14:00–15:00 Dentist (moved)") || !strings.Contains(d, "#3b82f6") || strings.Contains(d, "#22c55e") {
		t.Fatalf("after the edit's sync: %q", d)
	}
	// A Members-only change is GoCasa's own: it needs no Google.
	g.set(func(g *fakeGoogle) { g.writeFail = http.StatusServiceUnavailable })
	n = len(g.sent())
	members := url.Values{"title": {"Dentist (moved)"}, "start": {"2026-03-16T10:00"}, "end": {"2026-03-16T11:00"}, "tz": {"America/New_York"}, "members": {"2"}}
	lists := len(g.requests())
	if code, _ := submit(t, h, path, "/edit", members); code != http.StatusOK || len(g.sent()) != n || len(g.requests()) != lists {
		t.Fatalf("a Members-only change = %d, %d writes", code, len(g.sent())-n)
	}
	if d := day(t, agendaPage(t, h), "2026-03-16"); !strings.Contains(d, "#22c55e") || strings.Contains(d, "#3b82f6") {
		t.Fatalf("after a Members-only change: %q", d)
	}
}

// An edit made over a newer change in Google is refused, and the dialog shows
// Google's version to redo it on.
func TestGoogleStaleEditShowsTheCurrentVersion(t *testing.T) {
	g := newGoogle(t)
	h, _ := syncedWith(t, g, withEtag(gTimed("dentist", "Dentist", "2026-03-16T10:00:00Z", "2026-03-16T11:00:00Z"), "e1"))
	path := fmt.Sprintf("/calendar/synced/%d", eventID(t, h, "dentist"))
	g.set(func(g *fakeGoogle) {
		g.etags["dentist"] = "e2"
		g.changes["alice@example.com"] = []map[string]any{withEtag(gTimed("dentist", "Dentist (Google)", "2026-03-16T12:00:00Z", "2026-03-16T13:00:00Z"), "e2")}
	})
	code, page := submit(t, h, path, "/edit", googleEdit("Dentist (GoCasa)", "2026-03-16T14:00", "2026-03-16T15:00"))
	if code != http.StatusConflict || !strings.Contains(page, "changed in Google") || !strings.Contains(page, `value="Dentist (Google)"`) || !strings.Contains(page, `value="2026-03-16T08:00"`) {
		t.Fatalf("a stale edit = %d:\n%s", code, page)
	}
	if w := g.sent(); len(w) != 1 || w[0].ifMatch != "e1" {
		t.Fatalf("Google got %+v", w)
	}

	// Redone on the current version, it goes through.
	if code, _ := submit(t, h, path, "/edit", googleEdit("Dentist (GoCasa)", "2026-03-16T14:00", "2026-03-16T15:00")); code != http.StatusOK {
		t.Fatalf("the redone edit = %d", code)
	}
	if w := g.sent(); len(w) != 2 || w[1].ifMatch != "e2" {
		t.Fatalf("Google got %+v", w)
	}

	// A dialog opened before a sync brought a newer version is refused before Google sees it.
	_, page = h.Get(t, path)
	g.set(func(g *fakeGoogle) {
		g.changes["alice@example.com"] = []map[string]any{withEtag(gTimed("dentist", "Dentist (newer)", "2026-03-16T12:00:00Z", "2026-03-16T13:00:00Z"), "e3")}
	})
	h.Post(t, "/calendar/sync", nil)
	_, rest, _ := strings.Cut(page, `name="etag" value="`)
	shown, _, _ := strings.Cut(rest, `"`)
	for _, action := range []string{"/edit", "/delete"} {
		v := googleEdit("Dentist (old form)", "2026-03-16T14:00", "2026-03-16T15:00", "etag", shown)
		if code, page := h.Post(t, path+action, v); code != http.StatusConflict || !strings.Contains(page, "Dentist (newer)") || len(g.sent()) != 2 {
			t.Fatalf("%s from a dialog older than the last sync = %d:\n%s", action, code, page)
		}
	}

	// An event synced before GoCasa kept versions waits for one.
	h.App.DB.Exec("UPDATE events SET etag = NULL")
	if code, _ := submit(t, h, path, "/edit", googleEdit("Dentist (GoCasa)", "2026-03-16T14:00", "2026-03-16T15:00")); code != http.StatusConflict || len(g.sent()) != 2 {
		t.Fatalf("an edit without a version = %d", code)
	}
}

// Creating and deleting on a Google calendar go to Google; a read-only
// calendar refuses both.
func TestGoogleCreateAndDelete(t *testing.T) {
	g := newGoogle(t)
	h, alice := syncedWith(t, g, withEtag(gTimed("dentist", "Dentist", "2026-03-16T10:00:00Z", "2026-03-16T11:00:00Z"), "e1"))
	if _, page := h.Get(t, "/calendar/event/new"); !strings.Contains(page, ">Alice</option>") {
		t.Fatalf("the new event dialog lacks the Google calendar:\n%s", page)
	}
	g.set(func(g *fakeGoogle) {
		g.changes["alice@example.com"] = []map[string]any{withEtag(gTimed("new1", "Swim", "2026-03-17T10:00:00Z", "2026-03-17T11:00:00Z"), "n1")}
	})
	form := googleEdit("Swim", "2026-03-17T10:00", "2026-03-17T11:00", "calendar_id", fmt.Sprint(alice), "repeat", "weekly")
	if code, _ := h.Post(t, "/calendar/event", form); code != http.StatusOK {
		t.Fatalf("creating = %d", code)
	}
	w := g.sent()[0]
	if w.method != http.MethodPost || w.id != "" || w.body["summary"] != "Swim" || fmt.Sprint(w.body["recurrence"]) != "[RRULE:FREQ=WEEKLY;INTERVAL=1]" {
		t.Fatalf("Google got %+v", w)
	}
	if d := day(t, agendaPage(t, h), "2026-03-17"); !strings.Contains(d, "10:00–11:00 Swim") || !strings.Contains(d, "#22c55e") {
		t.Fatalf("the new event = %q", d)
	}
	var local int
	h.App.DB.QueryRow("SELECT count(*) FROM events WHERE calendar_id = ? AND external_id IS NULL", alice).Scan(&local)
	if local != 0 {
		t.Fatal("GoCasa stored its own copy of a Google event")
	}

	if code, _ := submit(t, h, fmt.Sprintf("/calendar/synced/%d", eventID(t, h, "dentist")), "/delete", nil); code != http.StatusOK {
		t.Fatalf("deleting = %d", code)
	}
	if w := g.sent()[1]; w.method != http.MethodDelete || w.id != "dentist" || w.ifMatch != "e1" {
		t.Fatalf("Google got %+v", w)
	}

	// Read-only: the dialog is Members only, and the API refuses changes.
	h.App.DB.Exec("UPDATE source_calendars SET read_only = 1 WHERE id = ?", alice)
	swim := fmt.Sprintf("/calendar/synced/%d", eventID(t, h, "new1"))
	if _, page := h.Get(t, swim); strings.Contains(page, `name="title"`) {
		t.Fatalf("a read-only event's dialog edits it:\n%s", page)
	}
	if _, page := h.Get(t, "/calendar/event/new"); strings.Contains(page, ">Alice</option>") {
		t.Fatal("a read-only calendar is offered for new events")
	}
	n := len(g.sent())
	for _, p := range []string{swim + "/edit", swim + "/delete"} {
		if code, _ := h.Post(t, p, googleEdit("Swim", "2026-03-17T10:00", "2026-03-17T11:00", "etag", "n1")); code != http.StatusBadRequest {
			t.Fatalf("%s on a read-only calendar = %d", p, code)
		}
	}
	if code, _ := h.Post(t, "/calendar/event", form); code != http.StatusBadRequest || len(g.sent()) != n {
		t.Fatalf("creating on a read-only calendar = %d, %d writes", code, len(g.sent())-n)
	}
}

// swimSeries is Google series swim: weekly at 10:00 UTC from 14 March, 10
// times, its first three occurrences synced, and Google answering GETs of it.
func swimSeries(t *testing.T, g *fakeGoogle) *apptest.Harness {
	t.Helper()
	var occs []map[string]any
	for i, d := range []string{"14", "21", "28"} {
		id := "swim_202603" + d
		o := withEtag(gOccurrence("swim", id, "Swim", "2026-03-"+d+"T10:00:00Z", "2026-03-"+d+"T11:00:00Z"), fmt.Sprintf("o%d", i))
		o["originalStartTime"] = map[string]any{"dateTime": "2026-03-" + d + "T10:00:00Z"}
		o["start"].(map[string]any)["timeZone"] = "Europe/London"
		occs = append(occs, o)
		g.got[id] = o
	}
	master := withEtag(gTimed("swim", "Swim", "2026-03-14T10:00:00Z", "2026-03-14T11:00:00Z"), "m1")
	master["start"].(map[string]any)["timeZone"] = "Europe/London"
	master["recurrence"] = []string{"RRULE:FREQ=WEEKLY;COUNT=10", "EXDATE;TZID=Europe/London:20260404T100000", "RDATE;TZID=Europe/London:20260401T100000"}
	g.got["swim"], g.etags["swim"] = master, "m1"
	h, _ := syncedWith(t, g, occs...)
	return h
}

// Each Edit scope on a Google Series writes the occurrence or the Series, and
// a Split creates the new Series before ending the old one.
func TestGoogleSeriesScopes(t *testing.T) {
	g := newGoogle(t)
	h := swimSeries(t, g)
	second := fmt.Sprintf("/calendar/synced/%d", eventID(t, h, "swim_20260321"))
	if _, page := h.Get(t, second); !strings.Contains(page, `name="scope" value="following"`) {
		t.Fatalf("an occurrence's dialog lacks the edit scopes:\n%s", page)
	}

	// This event: that occurrence alone.
	submit(t, h, second, "/edit", googleEdit("Swim (late)", "2026-03-21T11:00", "2026-03-21T12:00", "scope", "this"))
	if w := g.sent()[0]; w.method != http.MethodPatch || w.id != "swim_20260321" || w.ifMatch != "o1" {
		t.Fatalf("This event sent %+v", w)
	}

	// All events: the series' changed text only, on its etag; never times.
	if code, _ := submit(t, h, second, "/edit", googleEdit("Swim", "2026-03-21T11:00", "2026-03-21T12:00", "scope", "all")); code != http.StatusBadRequest {
		t.Fatalf("a retimed All events edit = %d", code)
	}
	submit(t, h, second, "/edit", googleEdit("Swimming", "2026-03-21T10:00", "2026-03-21T11:00", "scope", "all", "location", "Pool"))
	if w := g.sent()[len(g.sent())-1]; w.method != http.MethodPatch || w.id != "swim" || w.ifMatch != "m1" || len(w.body) != 2 || w.body["summary"] != "Swimming" || w.body["location"] != "Pool" {
		t.Fatalf("All events sent %+v", w)
	}

	// This and following: a new series from here, then the old one ends just before it.
	n := len(g.sent())
	if code, _ := submit(t, h, second, "/edit", googleEdit("Swim club", "2026-03-21T09:00", "2026-03-21T10:00", "scope", "following")); code != http.StatusOK {
		t.Fatalf("This and following = %d", code)
	}
	w := g.sent()[n:]
	if len(w) != 2 || w[0].method != http.MethodPost || w[0].body["summary"] != "Swim club" ||
		fmt.Sprint(w[0].body["recurrence"]) != "[RRULE:FREQ=WEEKLY;COUNT=9 EXDATE;TZID=Europe/London:20260404T090000]" ||
		w[1].method != http.MethodPatch || w[1].id != "swim" || w[1].ifMatch != "m1" ||
		fmt.Sprint(w[1].body["recurrence"]) != "[RRULE:FREQ=WEEKLY;UNTIL=20260321T095959Z EXDATE;TZID=Europe/London:20260404T100000 RDATE;TZID=Europe/London:20260401T100000]" {
		t.Fatalf("This and following sent %+v", w)
	}
	if q := g.requests()[len(g.requests())-1]; q.Has("syncToken") {
		t.Fatal("the sync after a Split wasn't a full re-import")
	}

	// If ending the old series fails, the new one goes.
	g.set(func(g *fakeGoogle) { g.writeFail, g.failOn = http.StatusForbidden, http.MethodPatch })
	n = len(g.sent())
	if code, page := submit(t, h, second, "/edit", googleEdit("Swim club", "2026-03-21T09:00", "2026-03-21T10:00", "scope", "following")); code != http.StatusBadGateway || !strings.Contains(page, "Google refused") {
		t.Fatalf("a Split Google half-refused = %d:\n%s", code, page)
	}
	if w := g.sent()[n:]; len(w) != 3 || w[2].method != http.MethodDelete || w[2].id != fmt.Sprintf("new%d", n+1) {
		t.Fatalf("a failed Split sent %+v", w)
	}

	// If taking the new one back fails too, the person hears so.
	g.set(func(g *fakeGoogle) { g.failOn = "PATCH DELETE" })
	if code, page := submit(t, h, second, "/edit", googleEdit("Swim club", "2026-03-21T09:00", "2026-03-21T10:00", "scope", "following")); code != http.StatusBadGateway || !strings.Contains(page, "fix the overlap in Google") {
		t.Fatalf("a Split left half-done = %d:\n%s", code, page)
	}

	// On the first occurrence, This and following is the whole series.
	g.set(func(g *fakeGoogle) { g.writeFail = 0 })
	first := fmt.Sprintf("/calendar/synced/%d", eventID(t, h, "swim_20260314"))
	n = len(g.sent())
	submit(t, h, first, "/edit", googleEdit("Swim", "2026-03-14T09:00", "2026-03-14T10:00", "scope", "following"))
	if w := g.sent()[n:]; len(w) != 1 || w[0].method != http.MethodPatch || w[0].id != "swim" || w[0].body["recurrence"] != nil {
		t.Fatalf("This and following from the first sent %+v", w)
	}

	// Deleting: This and following ends the series, All events deletes it.
	n = len(g.sent())
	submit(t, h, second, "/delete", url.Values{"scope": {"following"}})
	submit(t, h, second, "/delete", url.Values{"scope": {"all"}})
	w = g.sent()[n:]
	if len(w) != 2 || w[0].method != http.MethodPatch || !strings.Contains(fmt.Sprint(w[0].body["recurrence"]), "UNTIL=20260321T095959Z") ||
		w[1].method != http.MethodDelete || w[1].id != "swim" || w[1].ifMatch != "m1" {
		t.Fatalf("deleting sent %+v", w)
	}

	// An occurrence changed in Google since GoCasa saw it refuses series scopes before any write.
	g.set(func(g *fakeGoogle) { g.got["swim_20260321"]["etag"] = "o9" })
	n = len(g.sent())
	if code, _ := submit(t, h, second, "/edit", googleEdit("Swim", "2026-03-21T10:00", "2026-03-21T11:00", "scope", "all", "location", "Lido")); code != http.StatusConflict || len(g.sent()) != n {
		t.Fatalf("a stale occurrence's All events edit = %d", code)
	}
}

// A Split made in Google's own app arrives through sync like any other change.
func TestGoogleSplitMadeInGoogle(t *testing.T) {
	g := newGoogle(t)
	h := swimSeries(t, g)
	g.set(func(g *fakeGoogle) {
		g.changes["alice@example.com"] = []map[string]any{
			gCancelled("swim_20260321"), gCancelled("swim_20260328"),
			gOccurrence("club", "club_20260321", "Swim club", "2026-03-21T09:00:00Z", "2026-03-21T10:00:00Z"),
			gOccurrence("club", "club_20260328", "Swim club", "2026-03-28T09:00:00Z", "2026-03-28T10:00:00Z"),
		}
	})
	h.Post(t, "/calendar/sync", nil)
	page := agendaPage(t, h)
	if d := day(t, page, "2026-03-21"); strings.Contains(d, "10:00–11:00 Swim") || !strings.Contains(d, "09:00–10:00 Swim club") {
		t.Fatalf("21 March after a Split in Google: %q", d)
	}
	eventID(t, h, "swim_20260314") // the series before the Split stays
}

// Members changed This and following go on this Occurrence and later ones
// only, and never reach Google.
func TestGoogleMembersFromHereOn(t *testing.T) {
	g := newGoogle(t)
	h := swimSeries(t, g)
	second := fmt.Sprintf("/calendar/synced/%d", eventID(t, h, "swim_20260321"))
	if code, _ := submit(t, h, second, "/edit", googleEdit("Swim", "2026-03-21T10:00", "2026-03-21T11:00", "scope", "following", "members", "1")); code != http.StatusOK || len(g.sent()) != 0 {
		t.Fatalf("a Members-only This and following = %d, %d writes", code, len(g.sent()))
	}
	_, week := h.Get(t, "/calendar?view=week&date=2026-03-14")
	page := agendaPage(t, h)
	if !strings.Contains(day(t, week, "2026-03-14"), "#22c55e") || !strings.Contains(day(t, page, "2026-03-21"), "#3b82f6") || !strings.Contains(day(t, page, "2026-03-28"), "#3b82f6") {
		t.Fatalf("Members after This and following:\n%s\n%s", day(t, week, "2026-03-14"), page)
	}
}

// A Split lands through the full re-import after it: the old Series' later
// Occurrences go and the new Series arrives with the Members picked.
func TestGoogleSplitLands(t *testing.T) {
	g := newGoogle(t)
	h := swimSeries(t, g)
	second := fmt.Sprintf("/calendar/synced/%d", eventID(t, h, "swim_20260321"))
	g.set(func(g *fakeGoogle) {
		cal := g.events["alice@example.com"]
		g.events["alice@example.com"] = []map[string]any{cal[0], // what Google lists after the Split: new1 is the fake's id for the first write
			gOccurrence("new1", "new1_20260321", "Swim club", "2026-03-21T09:00:00Z", "2026-03-21T10:00:00Z"),
			gOccurrence("new1", "new1_20260328", "Swim club", "2026-03-28T09:00:00Z", "2026-03-28T10:00:00Z")}
	})
	if code, _ := submit(t, h, second, "/edit", googleEdit("Swim club", "2026-03-21T09:00", "2026-03-21T10:00", "scope", "following", "members", "1")); code != http.StatusOK {
		t.Fatalf("This and following = %d", code)
	}
	page := agendaPage(t, h)
	for _, d := range []string{"2026-03-21", "2026-03-28"} {
		if got := day(t, page, d); strings.Contains(got, "10:00–11:00 Swim") || !strings.Contains(got, "09:00–10:00 Swim club") || !strings.Contains(got, "#3b82f6") {
			t.Fatalf("%s after the Split: %q", d, got)
		}
	}
	eventID(t, h, "swim_20260314") // before the Split, unchanged
}
