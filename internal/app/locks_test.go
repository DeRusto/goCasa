package app_test

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/apptest"
)

func count(t *testing.T, h *apptest.Harness, query string) int {
	t.Helper()
	var n int
	if err := h.App.DB.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func asksForPIN(t *testing.T, h *apptest.Harness, path string, f url.Values) bool {
	t.Helper()
	_, body := h.Post(t, path, f)
	return strings.Contains(body, lockPage)
}

func TestLocksAreOffByDefaultAndOnlyAnAdminTurnsThemOn(t *testing.T) {
	h := household(t)
	post(t, h, "/todo-columns", url.Values{"name": {"House"}})
	post(t, h, "/todos", todo("1", "Fix tap", ""))

	other := h.Device()
	if asksForPIN(t, other, "/calendar/event", timed("Dentist", "2026-03-16T09:00", "2026-03-16T10:00", "UTC")) {
		t.Fatal("calendar asked for a PIN with its Lock off")
	}
	if asksForPIN(t, other, "/todos/1/complete", nil) {
		t.Fatal("to-dos asked for a PIN with their Lock off")
	}
	if !asksForPIN(t, other, "/settings/locks", url.Values{"lock_calendar": {"on"}, "lock_todos": {"on"}}) {
		t.Fatal("a locked device changed the Locks")
	}
	if n := count(t, h, "SELECT lock_calendar + lock_todos FROM household_settings"); n != 0 {
		t.Fatalf("locked device turned on %d Locks", n)
	}
	_, body := h.Get(t, "/settings")
	if !strings.Contains(body, `name="lock_calendar"`) || !strings.Contains(body, `name="lock_todos"`) {
		t.Fatalf("Settings has no Locks: %q", body)
	}
}

func TestCalendarLockGatesChangesButNotViewing(t *testing.T) {
	h := household(t)
	addEvent(t, h, timed("Dentist", "2026-03-16T09:00", "2026-03-16T10:00", "UTC"))
	post(t, h, "/settings/locks", url.Values{"lock_calendar": {"on"}})

	other := h.Device()
	if _, body := other.Get(t, "/calendar?date=2026-03-16"); strings.Contains(body, lockPage) || !strings.Contains(body, "Dentist") {
		t.Fatalf("viewing the calendar was locked: %q", body)
	}
	for _, c := range []struct {
		path string
		f    url.Values
	}{
		{"/calendar/event", timed("Vet", "2026-03-16T11:00", "2026-03-16T12:00", "UTC")},
		{"/calendar/event/1", timed("Moved", "2026-03-16T11:00", "2026-03-16T12:00", "UTC")},
		{"/calendar/event/1/delete", nil},
		{"/calendar/calendars", url.Values{"name": {"Work"}, "color": {"#000000"}}},
	} {
		if !asksForPIN(t, other, c.path, c.f) {
			t.Errorf("POST %s did not ask for a PIN", c.path)
		}
	}
	if n := count(t, h, "SELECT count(*) FROM events WHERE title = 'Dentist'"); n != 1 {
		t.Fatalf("locked calendar changed: %d Dentist events", n)
	}
	// Shopping lists and to-dos aren't behind the calendar Lock.
	post(t, other, "/todo-columns", url.Values{"name": {"House"}})

	unlock(t, other, 1, "1234")
	addEvent(t, other, timed("Vet", "2026-03-16T11:00", "2026-03-16T12:00", "UTC"))
}

func TestToDoLockGatesCompletingOnly(t *testing.T) {
	h := apptest.New(t)
	setup(t, h)
	post(t, h, "/todo-columns", url.Values{"name": {"House"}})
	post(t, h, "/todos", todo("1", "Fix tap", ""))
	post(t, h, "/settings/locks", url.Values{"lock_todos": {"on"}})

	other := h.Device()
	if !asksForPIN(t, other, "/todos/1/complete", nil) {
		t.Fatal("completing a to-do did not ask for a PIN")
	}
	if n := count(t, h, "SELECT count(*) FROM todos WHERE completed_at IS NOT NULL"); n != 0 {
		t.Fatal("locked to-do was completed")
	}
	post(t, other, "/todos", todo("1", "Paint fence", ""))
	// The calendar Lock is still off.
	if asksForPIN(t, other, "/calendar/calendars", url.Values{"name": {"Family"}, "color": {"#a855f7"}}) {
		t.Fatal("calendar asked for a PIN with only the to-do Lock on")
	}

	unlock(t, other, 1, "1234")
	post(t, other, "/todos/1/complete", nil)
	if count(t, h, "SELECT count(*) FROM todos WHERE completed_at IS NOT NULL") != 1 {
		t.Fatal("unlocked device could not complete a to-do")
	}
}

func TestOneUnlockCoversEveryLockUntilFiveIdleMinutes(t *testing.T) {
	h := household(t)
	post(t, h, "/todo-columns", url.Values{"name": {"House"}})
	post(t, h, "/todos", todo("1", "Fix tap", ""))
	post(t, h, "/todos", todo("1", "Paint fence", ""))
	post(t, h, "/settings/locks", url.Values{"lock_calendar": {"on"}, "lock_todos": {"on"}})

	other := h.Device()
	unlock(t, other, 1, "1234")
	h.Clock.Advance(4 * time.Minute)
	if asksForPIN(t, other, "/todos/1/complete", nil) {
		t.Fatal("unlock did not cover the to-do Lock")
	}
	h.Clock.Advance(4 * time.Minute) // 4 idle minutes since the last request
	if asksForPIN(t, other, "/calendar/event", timed("Vet", "2026-03-16T11:00", "2026-03-16T12:00", "UTC")) {
		t.Fatal("unlock did not cover the calendar Lock")
	}
	if _, body := other.Get(t, "/settings"); !strings.Contains(body, "<h1>Settings</h1>") {
		t.Fatal("unlock did not cover Settings")
	}

	h.Clock.Advance(5 * time.Minute)
	if !asksForPIN(t, other, "/todos/2/complete", nil) {
		t.Fatal("to-do Lock did not relock after 5 idle minutes")
	}
	if !asksForPIN(t, other, "/calendar/event/1/delete", nil) {
		t.Fatal("calendar Lock did not relock after 5 idle minutes")
	}
}
