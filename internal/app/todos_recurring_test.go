package app_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strconv"
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// A to-do due monthly on the 31st falls on the last day of shorter months,
// and goes back to the 31st after them.
func TestMonthlyAndYearlyRepeatsClampToTheMonthsLastDay(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	h.Clock.Set(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)) // every completion is early
	post(t, h, "/todo-columns", url.Values{"name": {"House"}})
	post(t, h, "/todos", recurringTodo("Rent", "2028-01-31", "monthly", nil))
	for id := 1; id <= 4; id++ {
		post(t, h, "/todos/"+strconv.Itoa(id)+"/complete", nil)
	}
	if got, want := board(t, h, "status=all"), "Rent 2028-01-31|Rent 2028-02-29|Rent 2028-03-31|Rent 2028-04-30|Rent 2028-05-31"; got != want {
		t.Fatalf("31st = %q, want %q", got, want)
	}

	post(t, h, "/todos", recurringTodo("Rates", "2027-01-30", "monthly", nil)) // 6
	post(t, h, "/todos", recurringTodo("Leap", "2028-02-29", "yearly", nil))   // 7
	post(t, h, "/todos", recurringTodo("Plain", "2027-01-15", "monthly", nil)) // 8
	for _, id := range []string{"6", "9", "7", "11", "12", "13"} {             // Rates 6, 9, 10; Leap 7, 11 to 14
		post(t, h, "/todos/"+id+"/complete", nil)
	}
	if got, want := board(t, h, "status=all"), "Plain 2027-01-15|Rates 2027-01-30|Rates 2027-02-28|Rates 2027-03-30|"+
		"Rent 2028-01-31|Rent 2028-02-29|Leap 2028-02-29|Rent 2028-03-31|Rent 2028-04-30|Rent 2028-05-31|"+
		"Leap 2029-02-28|Leap 2030-02-28|Leap 2031-02-28|Leap 2032-02-29"; got != want {
		t.Fatalf("all = %q, want %q", got, want)
	}
	var rule string
	h.App.DB.QueryRow("SELECT repeat_rule FROM todos WHERE id = 8").Scan(&rule)
	if rule != "FREQ=MONTHLY;INTERVAL=1" {
		t.Errorf("15th rule = %q, want no anchor", rule)
	}
	for id, want := range map[string]string{"1": "monthly", "7": "yearly"} {
		if _, body := h.Get(t, "/todos/"+id); !strings.Contains(body, `value="`+want+`" selected`) {
			t.Errorf("edit form of %s doesn't show %s", id, want)
		}
	}
}

// Repeat from completion anchors to the completion day, so a to-do done on
// the 31st lands on a short month's last day and back on the 31st after it.
func TestRepeatFromCompletionAnchorsToTheCompletionDay(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	h.Clock.Set(time.Date(2026, 12, 31, 12, 0, 0, 0, time.UTC))
	post(t, h, "/todo-columns", url.Values{"name": {"House"}})
	post(t, h, "/todos", recurringTodo("Filter", "2026-12-20", "monthly", url.Values{"repeat_from": {"completion"}, "interval": {"2"}}))
	post(t, h, "/todos/1/complete", nil) // done Dec 31: two months on is Feb 28
	h.Clock.Set(time.Date(2027, 2, 15, 12, 0, 0, 0, time.UTC))
	post(t, h, "/todos/2/complete", nil) // done early on the 15th: the anchor moves
	if got, want := board(t, h, "status=all"), "Filter 2026-12-20|Filter 2027-02-28|Filter 2027-04-15"; got != want {
		t.Fatalf("all = %q, want %q", got, want)
	}
	var rule2, rule3 string
	h.App.DB.QueryRow("SELECT repeat_rule FROM todos WHERE id = 2").Scan(&rule2)
	h.App.DB.QueryRow("SELECT repeat_rule FROM todos WHERE id = 3").Scan(&rule3)
	if rule2 != "FREQ=MONTHLY;INTERVAL=2;BYMONTHDAY=28,29,30,31;BYSETPOS=-1" || rule3 != "FREQ=MONTHLY;INTERVAL=2" {
		t.Errorf("rules = %q, %q, want the 31st anchor then none", rule2, rule3)
	}
}

// A chain made before the anchor existed gains it from its due day.
func TestAChainWithoutTheAnchorGainsItOnItsNextCompletion(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	h.Clock.Set(time.Date(2027, 1, 1, 12, 0, 0, 0, time.UTC))
	post(t, h, "/todo-columns", url.Values{"name": {"House"}})
	post(t, h, "/todos", recurringTodo("Rent", "2027-03-31", "monthly", nil))
	if _, err := h.App.DB.Exec("UPDATE todos SET repeat_rule = 'FREQ=MONTHLY;INTERVAL=1' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	post(t, h, "/todos/1/complete", nil)
	var rule string
	h.App.DB.QueryRow("SELECT repeat_rule FROM todos WHERE id = 2").Scan(&rule)
	if got, want := board(t, h, ""), "Rent 2027-04-30"; got != want || rule != "FREQ=MONTHLY;INTERVAL=1;BYMONTHDAY=28,29,30,31;BYSETPOS=-1" {
		t.Fatalf("next = %q with %q, want %q anchored to the 31st", got, rule, want)
	}
}
