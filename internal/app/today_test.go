package app_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// todayAgendaItem is the <li> on page whose text contains title.
func todayAgendaItem(t *testing.T, section, title string) string {
	t.Helper()
	i := strings.Index(section, title)
	if i < 0 {
		t.Fatalf("no %q in %q", title, section)
	}
	end := strings.Index(section[i:], "</li>")
	if end < 0 {
		t.Fatalf("%q isn't in a list item", title)
	}
	return section[strings.LastIndex(section[:i], "<li") : i+end]
}

func TestTodayShowsWeatherAgendaAndDueToDosForEveryone(t *testing.T) {
	h, m := weatherHousehold(t) // now is 2026-03-15 09:00 UTC
	h.Post(t, "/settings/members", member("Bob", "#22c55e", "member", ""))
	post(t, h, "/calendar/calendars", url.Values{"name": {"Family"}, "color": {"#a855f7"}})
	post(t, h, "/settings/weather", url.Values{"lat": {"51.5"}, "lon": {"-0.12"}, "units": {"metric"}})
	h.RunDue()
	m.Close() // Today renders from the cache alone

	addEvent(t, h, timed("Breakfast", "2026-03-15T07:00", "2026-03-15T08:00", "UTC", "2"))
	addEvent(t, h, timed("Dentist", "2026-03-15T15:00", "2026-03-15T16:00", "UTC"))
	addEvent(t, h, allDay("Market day", "2026-03-15", "2026-03-15"))
	addEvent(t, h, timed("Swim", "2026-03-16T10:00", "2026-03-16T11:00", "UTC"))
	addEvent(t, h, timed("Too far", "2026-03-17T10:00", "2026-03-17T11:00", "UTC"))
	addEvent(t, h, timed("Yesterday", "2026-03-14T10:00", "2026-03-14T11:00", "UTC"))

	post(t, h, "/todo-columns", url.Values{"name": {"House"}})
	post(t, h, "/todos", todo("1", "Pay rent", "2026-03-10"))
	post(t, h, "/todos", todo("1", "Water plants", "2026-03-15"))
	post(t, h, "/todos", todo("1", "Next week", "2026-03-22"))
	post(t, h, "/todos", todo("1", "Undated", ""))
	post(t, h, "/todos", todo("1", "Done already", "2026-03-12"))
	post(t, h, "/todos/5/complete", nil)

	code, page := h.Get(t, "/today")
	if code != http.StatusOK {
		t.Fatalf("GET /today = %d", code)
	}

	// Large header: clock, date, current conditions, today's high/low and the days ahead.
	header, _, _ := strings.Cut(page, "</header>")
	for _, want := range []string{"09:00", "Sunday 15 March", "12°C", "Overcast", "H 16° L 8°", "Mon", "Clear", "17°"} {
		if !strings.Contains(header, want) {
			t.Errorf("header lacks %q: %q", want, header)
		}
	}
	if strings.Contains(header, "Sat") {
		t.Error("forecast shows yesterday")
	}
	if strings.Contains(page, `class="weather-pill"`) {
		t.Error("Today shows the slim pill")
	}

	today, tomorrow := day(t, page, "2026-03-15"), day(t, page, "2026-03-16")
	for _, title := range []string{"Breakfast", "Dentist", "Market day"} {
		if !strings.Contains(today, title) {
			t.Errorf("today lacks %s", title)
		}
	}
	if !strings.Contains(todayAgendaItem(t, today, "Breakfast"), "past") {
		t.Error("finished event not greyed")
	}
	if strings.Contains(todayAgendaItem(t, today, "Dentist"), "past") || strings.Contains(todayAgendaItem(t, today, "Market day"), "past") {
		t.Error("event still to come is greyed")
	}
	if !strings.Contains(tomorrow, "Swim") || strings.Contains(page, "Too far") || strings.Contains(page, "Yesterday") {
		t.Errorf("agenda isn't today and tomorrow: %q", page)
	}

	_, todos, _ := strings.Cut(page, `id="today-todos"`)
	todos, _, _ = strings.Cut(todos, "</section>")
	if strings.Index(todos, "Pay rent") < 0 || strings.Index(todos, "Pay rent") > strings.Index(todos, "Water plants") {
		t.Errorf("to-dos = %q", todos)
	}
	if !strings.Contains(todayAgendaItem(t, todos, "Pay rent"), "Overdue") || strings.Contains(todayAgendaItem(t, todos, "Water plants"), "Overdue") {
		t.Errorf("overdue marks wrong: %q", todos)
	}
	for _, not := range []string{"Next week", "Undated", "Done already"} {
		if strings.Contains(todos, not) {
			t.Errorf("to-dos show %s", not)
		}
	}

	if strings.Contains(page, `name="users"`) {
		t.Error("Today has a Member filter")
	}
	if !strings.Contains(page, `id="live"`) || !strings.Contains(page, "/events/stream") {
		t.Error("Today doesn't live-update")
	}
}

func TestTodayWithoutWeatherStillRenders(t *testing.T) {
	h, _ := weatherHousehold(t)
	code, page := h.Get(t, "/today")
	if code != http.StatusOK || !strings.Contains(page, "Sunday 15 March") || !strings.Contains(page, "Nothing due") {
		t.Fatalf("GET /today = %d %q", code, page)
	}
}

func TestEveryPageLinksToTheOthersButNotTheScreensaver(t *testing.T) {
	h := household(t)
	for _, path := range []string{"/today", "/calendar", "/todos", "/lists", "/chores", "/settings"} {
		_, body := h.Get(t, path)
		for _, link := range []string{`href="/today"`, `href="/calendar"`, `href="/todos"`, `href="/lists"`, `href="/chores"`, `href="/settings"`} {
			if !strings.Contains(body, link) {
				t.Errorf("%s has no %s", path, link)
			}
		}
	}
	if _, body := h.Get(t, "/screensaver"); strings.Contains(body, "<nav") {
		t.Error("the screensaver has the nav")
	}
}
