package app_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/apptest"
)

var todoDue = regexp.MustCompile(`data-todo="\d+"[^>]*>\s*<a[^>]*>([^<]+)</a>\s*(?:<span>\s*due (\S+))?`)

// board is "title due" for each to-do /todos?q shows, by due date.
func board(t *testing.T, h *apptest.Harness, q string) string {
	t.Helper()
	_, page := h.Get(t, "/todos?sort=due&"+q)
	var out []string
	for _, m := range todoDue.FindAllStringSubmatch(page, -1) {
		out = append(out, strings.TrimSpace(m[1]+" "+m[2]))
	}
	return strings.Join(out, "|")
}

func recurringTodo(title, due, repeat string, extra url.Values) url.Values {
	f := todo("1", title, due)
	f.Set("repeat", repeat)
	for k, v := range extra {
		f[k] = v
	}
	return f
}

func TestRepeatFromScheduleSkipsMissedDatesAndIsNotConsumedByEarlyCompletion(t *testing.T) {
	h := apptest.New(t) // today is Sunday 2026-03-15
	post(t, h, "/todo-columns", url.Values{"name": {"House"}})
	post(t, h, "/todos", recurringTodo("Bins", "2026-03-02", "weekly", nil)) // Mondays, two missed
	post(t, h, "/todos", recurringTodo("Water", "2026-03-20", "daily", url.Values{"interval": {"2"}}))

	post(t, h, "/todos/1/complete", nil)
	post(t, h, "/todos/2/complete", nil) // five days early
	if got, want := board(t, h, ""), "Bins 2026-03-16|Water 2026-03-22"; got != want {
		t.Fatalf("open = %q, want %q", got, want)
	}
	if got, want := board(t, h, "status=done"), "Bins 2026-03-02|Water 2026-03-20"; got != want {
		t.Fatalf("done = %q, want %q", got, want)
	}
}

func TestRepeatFromCompletionAndTheDefaultDueDate(t *testing.T) {
	h := apptest.New(t) // today is Sunday 2026-03-15
	post(t, h, "/todo-columns", url.Values{"name": {"House"}})
	post(t, h, "/todos", recurringTodo("Gym", "", "weekly", url.Values{"byday": {"WE", "FR"}}))
	post(t, h, "/todos", recurringTodo("Filter", "2026-03-01", "monthly", url.Values{"repeat_from": {"completion"}}))
	post(t, h, "/todos", recurringTodo("Rent", "", "monthly", nil))
	if code, _ := h.Post(t, "/todos", recurringTodo("Bad", "", "hourly", nil)); code != http.StatusBadRequest {
		t.Fatalf("unknown repeat = %d, want 400", code)
	}
	if code, _ := h.Post(t, "/todos", recurringTodo("Bad", "", "daily", url.Values{"interval": {"0"}})); code != http.StatusBadRequest {
		t.Fatalf("interval 0 = %d, want 400", code)
	}

	post(t, h, "/todos/2/complete", nil)
	if got, want := board(t, h, ""), "Rent 2026-03-15|Gym 2026-03-18|Filter 2026-04-15"; got != want {
		t.Fatalf("open = %q, want %q", got, want)
	}
}

func TestCompletingTwiceCreatesTheNextCopyOnce(t *testing.T) {
	h := apptest.New(t)
	post(t, h, "/todo-columns", url.Values{"name": {"House"}})
	post(t, h, "/todos", recurringTodo("Bins", "2026-03-16", "weekly", nil))
	post(t, h, "/todos/1/complete", nil)
	post(t, h, "/todos/1/complete", nil)
	if got, want := board(t, h, "status=all"), "Bins 2026-03-16|Bins 2026-03-23"; got != want {
		t.Fatalf("all = %q, want %q", got, want)
	}
	if code, _ := h.Post(t, "/todos/99/complete", nil); code != http.StatusNotFound {
		t.Fatalf("unknown to-do = %d, want 404", code)
	}
}

func TestUncompletingDeletesAnOpenNextCopyAndRefusesACompletedOne(t *testing.T) {
	h := apptest.New(t)
	post(t, h, "/todo-columns", url.Values{"name": {"House"}})
	post(t, h, "/todos", recurringTodo("Bins", "2026-03-16", "weekly", nil))
	post(t, h, "/todos/1/complete", nil) // creates 2
	post(t, h, "/todos/1/reopen", nil)
	if got, want := board(t, h, "status=all"), "Bins 2026-03-16"; got != want {
		t.Fatalf("after re-open = %q, want %q", got, want)
	}

	post(t, h, "/todos/1/complete", nil) // creates 3
	post(t, h, "/todos/3/complete", nil) // creates 4
	if code, body := h.Post(t, "/todos/1/reopen", nil); code != http.StatusConflict || !strings.Contains(body, "re-open the later one first") {
		t.Fatalf("re-open with a completed next copy = %d %q, want 409", code, body)
	}
	post(t, h, "/todos/3/reopen", nil) // deletes 4
	post(t, h, "/todos/1/reopen", nil) // deletes 3
	if got, want := board(t, h, "status=all"), "Bins 2026-03-16"; got != want {
		t.Fatalf("after re-opening both = %q, want %q", got, want)
	}
}

func TestEditsCarryForwardAndCompletedCopiesAreKept(t *testing.T) {
	h := apptest.New(t)
	post(t, h, "/todo-columns", url.Values{"name": {"House"}})
	post(t, h, "/todos", recurringTodo("Bins", "2026-03-16", "weekly", nil))
	post(t, h, "/todos/1", recurringTodo("Recycling", "2026-03-16", "weekly", nil))
	post(t, h, "/todos/1/complete", nil) // creates 2
	if code, body := h.Get(t, "/todos/2"); code != http.StatusOK || !strings.Contains(body, `value="Recycling"`) || !strings.Contains(body, `value="weekly" selected`) {
		t.Fatalf("next copy = %d %q", code, body)
	}

	post(t, h, "/todos/2", recurringTodo("Glass", "2026-03-23", "daily", nil)) // the rule change applies at its completion
	post(t, h, "/todos/2/complete", nil)                                       // creates 3
	post(t, h, "/todos/3", todo("1", "Last glass", "2026-03-24"))              // repeat off ends the chain
	post(t, h, "/todos/3/complete", nil)
	if got, want := board(t, h, "status=all"), "Recycling 2026-03-16|Glass 2026-03-23|Last glass 2026-03-24"; got != want {
		t.Fatalf("all = %q, want %q", got, want)
	}
}

// A month from January 31 is February's last day, not early March.
func TestRepeatFromCompletionClampsToTheMonthsLastDay(t *testing.T) {
	h := apptest.New(t)
	h.Clock.Set(time.Date(2026, 1, 31, 12, 0, 0, 0, time.UTC))
	post(t, h, "/todo-columns", url.Values{"name": {"House"}})
	post(t, h, "/todos", recurringTodo("Filter", "2026-01-31", "monthly", url.Values{"repeat_from": {"completion"}}))
	post(t, h, "/todos", recurringTodo("Boiler", "2026-01-31", "yearly", url.Values{"repeat_from": {"completion"}}))
	post(t, h, "/todos/1/complete", nil)
	h.Clock.Set(time.Date(2028, 2, 29, 12, 0, 0, 0, time.UTC))
	post(t, h, "/todos/2", recurringTodo("Boiler", "2028-02-29", "yearly", url.Values{"repeat_from": {"completion"}}))
	post(t, h, "/todos/2/complete", nil)
	if got, want := board(t, h, ""), "Filter 2026-02-28|Boiler 2029-02-28"; got != want {
		t.Fatalf("open = %q, want %q", got, want)
	}
}

// Deleting a copy whose completion made the next one would cut the chain and
// let re-opening its predecessor leave two open copies.
func TestACompletedCopyWithANextCopyCannotBeDeleted(t *testing.T) {
	h := apptest.New(t)
	post(t, h, "/todo-columns", url.Values{"name": {"House"}})
	post(t, h, "/todos", recurringTodo("Bins", "2026-03-16", "weekly", nil))
	post(t, h, "/todos/1/complete", nil) // creates 2
	post(t, h, "/todos/2/complete", nil) // creates 3
	if code, _ := h.Post(t, "/todos/2/delete", nil); code != http.StatusConflict {
		t.Fatalf("deleting a copy with a next copy = %d, want 409", code)
	}
	post(t, h, "/todos/3/delete", nil)
	post(t, h, "/todos/2/delete", nil) // no next copy any more
	if got, want := board(t, h, "status=all"), "Bins 2026-03-16"; got != want {
		t.Fatalf("all = %q, want %q", got, want)
	}
}
