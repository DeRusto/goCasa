package app_test

import (
	"html"
	"net/url"
	"strings"
	"testing"

	"github.com/DeRusto/goCasa/internal/apptest"
)

// opens is the <title> of the page path ends up on, after redirects.
func opens(t *testing.T, get func(testing.TB, string) (int, string), path string) string {
	t.Helper()
	_, body := get(t, path)
	_, title, _ := strings.Cut(body, "<title>")
	title, _, _ = strings.Cut(title, "</title>")
	return title
}

func TestANewHouseholdOpensOnToday(t *testing.T) {
	t.Parallel()
	h := household(t)
	if got := opens(t, h.Get, "/"); got != "Today" {
		t.Fatalf("/ opened %q, want Today", got)
	}
}

// setDefault presses a page's "Set as default view on this device".
func setDefault(t *testing.T, h *apptest.Harness, page string) {
	t.Helper()
	_, body := h.Get(t, page)
	_, v, ok := strings.Cut(body, `name="default_view" value="`)
	if !ok {
		t.Fatalf("%s has no Set as default view: %q", page, body)
	}
	v, _, _ = strings.Cut(v, `"`)
	post(t, h, "/device/default-view", url.Values{"default_view": {html.UnescapeString(v)}})
}

func TestADeviceOpensOnItsOwnDefaultViewAtToday(t *testing.T) {
	t.Parallel()
	h := household(t) // today is Sunday 15 March 2026
	other := h.Device()
	setDefault(t, other, "/calendar?view=week&date=2026-04-01&users=2")

	_, body := other.Get(t, "/")
	if !strings.Contains(body, ">Week of 15 March 2026</h1>") {
		t.Fatalf("/ didn't open the week at today: %q", body)
	}
	if !strings.Contains(body, `value="2" checked`) {
		t.Fatalf("/ lost the Member filter: %q", body)
	}
	if got := opens(t, h.Get, "/"); got != "Today" {
		t.Fatalf("another device opened %q, want Today", got)
	}

	// Following a ?date=/?users= link leaves the default alone.
	other.Get(t, "/calendar?view=day&date=2026-05-01&users=3")
	if _, body := other.Get(t, "/"); !strings.Contains(body, ">Week of 15 March 2026</h1>") {
		t.Fatalf("a link changed the default: %q", body)
	}

	for page, title := range map[string]string{"/today": "Today", "/todos": "To-dos", "/chores": "Chores", "/lists": "Lists"} {
		setDefault(t, other, page)
		if got := opens(t, other.Get, "/"); got != title {
			t.Errorf("after setting %s, / opened %q", page, got)
		}
	}
}

func TestThisDeviceShowsItsDefaultViewAndClearsIt(t *testing.T) {
	t.Parallel()
	h := household(t)
	_, body := h.Get(t, "/settings")
	if !strings.Contains(body, "This device") || !strings.Contains(body, "Household default") {
		t.Fatalf("This device has no Default view: %q", body)
	}
	setDefault(t, h, "/todos")
	if _, body := h.Get(t, "/settings"); !strings.Contains(body, "/todos") {
		t.Fatalf("This device doesn't show /todos: %q", body)
	}
	post(t, h, "/device/default-view", url.Values{"default_view": {""}}) // Clear
	if got := opens(t, h.Get, "/"); got != "Today" {
		t.Fatalf("after Clear / opened %q, want Today", got)
	}
}

func TestThemeIsAutoUntilThisDevicePicksOne(t *testing.T) {
	t.Parallel()
	h := household(t)
	if _, body := h.Get(t, "/today"); !strings.Contains(body, `<meta name="color-scheme" content="light dark">`) {
		t.Fatalf("default theme isn't Auto: %q", body)
	}
	post(t, h, "/device/theme", url.Values{"theme": {"dark"}})
	if _, body := h.Get(t, "/calendar"); !strings.Contains(body, `<meta name="color-scheme" content="dark">`) {
		t.Fatalf("Dark theme not applied: %q", body)
	}
	if _, body := h.Device().Get(t, "/today"); !strings.Contains(body, `content="light dark"`) {
		t.Fatal("another device took this device's theme")
	}
	if _, body := h.Get(t, "/settings"); !strings.Contains(body, `<option value="dark" selected>`) {
		t.Fatalf("This device doesn't show the theme: %q", body)
	}
}

func TestHouseholdClockAndWeekStartApplyEverywhere(t *testing.T) {
	t.Parallel()
	h := household(t) // now is Sunday 15 March 2026 09:00 UTC
	addEvent(t, h, timed("Dentist", "2026-03-15T15:00", "2026-03-15T16:30", "UTC"))
	if _, body := h.Get(t, "/calendar?view=week"); !strings.Contains(body, "Week of 15 March 2026") || !strings.Contains(text(body), "15:00–16:30") {
		t.Fatalf("defaults aren't Sunday and 24-hour: %q", body)
	}
	post(t, h, "/settings/household", url.Values{"default_view": {"today"}, "week_start": {"1"}})

	_, body := h.Get(t, "/calendar?view=week")
	if !strings.Contains(body, "Week of 9 March 2026") || !strings.Contains(body, "3:00 PM–4:30 PM") {
		t.Fatalf("week view ignores Monday start or 12-hour clock: %q", body)
	}
	if _, body := h.Get(t, "/calendar"); !strings.Contains(body, "<th>Mon</th><th>Tue</th>") {
		t.Fatalf("month view doesn't start on Monday: %q", body)
	}
	_, body = h.Get(t, "/today")
	_, header, _ := strings.Cut(body, `id="live"`) // past the top bar
	header, _, _ = strings.Cut(header, "</header>")
	if !strings.Contains(header, ">9:00 AM</time>") || !strings.Contains(body, "3:00 PM–4:30 PM") {
		t.Fatalf("Today ignores the 12-hour clock: %q", body)
	}
}

func TestHouseholdDefaultViewIsTheFallback(t *testing.T) {
	t.Parallel()
	h := household(t)
	post(t, h, "/settings/household", url.Values{"default_view": {"lists"}, "week_start": {"0"}, "clock_24h": {"on"}})
	if got := opens(t, h.Device().Get, "/"); got != "Lists" {
		t.Fatalf("/ opened %q, want the Household's Lists", got)
	}
	if _, body := h.Get(t, "/settings"); !strings.Contains(body, `<option value="lists" selected>`) {
		t.Fatalf("Settings doesn't show the Household default view: %q", body)
	}
	if !asksForPIN(t, h.Device(), "/settings/household", url.Values{"default_view": {"todos"}, "week_start": {"0"}}) {
		t.Fatal("a locked device changed household settings")
	}
}
