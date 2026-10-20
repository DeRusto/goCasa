package app

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
)

func date(s string) time.Time {
	d, err := time.Parse(dateLayout, s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestEveryPresetRoundTripsThroughItsRule(t *testing.T) {
	for _, p := range []todoRepeat{
		{Freq: "daily", Interval: 1},
		{Freq: "daily", Interval: 3},
		{Freq: "weekly", Interval: 1},
		{Freq: "weekly", Interval: 2, ByDay: []string{"MO", "TH"}},
		{Freq: "monthly", Interval: 1},
		{Freq: "monthly", Interval: 6},
		{Freq: "yearly", Interval: 1},
	} {
		for _, due := range []string{"2026-03-15", "2026-01-31", "2028-02-29"} {
			rule := p.Encode(date(due))
			got := decodeRepeat(rule)
			if got.Freq != p.Freq || got.Interval != p.Interval || !slices.Equal(got.ByDay, p.ByDay) {
				t.Errorf("%+v on %s: %q decodes to %+v", p, due, rule, got)
			}
			if again := got.Encode(date("2026-03-15")); again != rule {
				t.Errorf("%q re-encodes as %q: the anchor is lost", rule, again)
			}
		}
	}
	if rule := (todoRepeat{}).Encode(date("2026-03-15")); rule != "" {
		t.Errorf("no repeat encodes as %q", rule)
	}
}

func TestEncodeAnchorsMonthEnds(t *testing.T) {
	for _, c := range []struct {
		p    todoRepeat
		due  string
		want string
	}{
		{todoRepeat{Freq: "monthly", Interval: 1}, "2026-03-15", "FREQ=MONTHLY;INTERVAL=1"},
		{todoRepeat{Freq: "monthly", Interval: 1}, "2026-01-31", "FREQ=MONTHLY;INTERVAL=1;BYMONTHDAY=28,29,30,31;BYSETPOS=-1"},
		{todoRepeat{Freq: "monthly", Interval: 1}, "2026-04-30", "FREQ=MONTHLY;INTERVAL=1;BYMONTHDAY=28,29,30;BYSETPOS=-1"},
		{todoRepeat{Freq: "yearly", Interval: 1}, "2028-02-29", "FREQ=YEARLY;INTERVAL=1;BYMONTH=2;BYMONTHDAY=28,29;BYSETPOS=-1"},
		{todoRepeat{Freq: "yearly", Interval: 1}, "2026-01-31", "FREQ=YEARLY;INTERVAL=1"},
	} {
		if got := c.p.Encode(date(c.due)); got != c.want {
			t.Errorf("%+v on %s = %q, want %q", c.p, c.due, got, c.want)
		}
	}
}

func TestNext(t *testing.T) {
	monthly, yearly := todoRepeat{Freq: "monthly", Interval: 1}, todoRepeat{Freq: "yearly", Interval: 1}
	for _, c := range []struct {
		name            string
		p               todoRepeat
		due, completed  string
		fromCompletion  bool
		wantDue, chain2 string // the next due date, and the one after it when completed on that day
	}{
		{"schedule skips missed dates", todoRepeat{Freq: "weekly", Interval: 1}, "2026-03-02", "2026-03-20", false, "2026-03-23", "2026-03-30"},
		{"schedule early completion keeps the next date", todoRepeat{Freq: "weekly", Interval: 1}, "2026-03-16", "2026-03-10", false, "2026-03-23", "2026-03-30"},
		{"schedule weekly by day", todoRepeat{Freq: "weekly", Interval: 1, ByDay: []string{"MO", "TH"}}, "2026-03-16", "2026-03-16", false, "2026-03-19", "2026-03-23"},
		{"schedule month end keeps to the 31st", monthly, "2026-01-31", "2026-01-31", false, "2026-02-28", "2026-03-31"},
		{"schedule the 30th", monthly, "2026-01-30", "2026-01-30", false, "2026-02-28", "2026-03-30"},
		{"schedule leap day", yearly, "2028-02-29", "2028-02-29", false, "2029-02-28", "2030-02-28"},
		{"completion daily", todoRepeat{Freq: "daily", Interval: 3}, "2026-03-01", "2026-03-15", true, "2026-03-18", "2026-03-21"},
		{"completion weekly", todoRepeat{Freq: "weekly", Interval: 2}, "2026-03-01", "2026-03-15", true, "2026-03-29", "2026-04-12"},
		{"completion month end clamps", monthly, "2026-01-02", "2026-01-31", true, "2026-02-28", "2026-03-28"},
		{"completion leap day clamps", yearly, "2027-01-01", "2028-02-29", true, "2029-02-28", "2030-02-28"},
	} {
		next, rule, err := c.p.Next(date(c.due), date(c.completed), c.fromCompletion)
		if err != nil || next != c.wantDue {
			t.Errorf("%s: Next = %q, %v; want %q", c.name, next, err, c.wantDue)
			continue
		}
		after, _, err := decodeRepeat(rule).Next(date(next), date(next), c.fromCompletion)
		if err != nil || after != c.chain2 {
			t.Errorf("%s: the copy after %s (rule %q) is due %q, %v; want %q", c.name, next, rule, after, err, c.chain2)
		}
	}
}

// board is a to-do module with one column (id 1).
func board(t *testing.T) todos {
	t.Helper()
	a := household(t) // today is 2026-03-15
	if _, err := a.DB.Exec("INSERT INTO todo_columns (name, position) VALUES ('Home', 0)"); err != nil {
		t.Fatal(err)
	}
	return todos{a}
}

// open is "title due" for each open to-do.
func open(t *testing.T, m todos) []string {
	t.Helper()
	ts, err := m.List(context.Background(), todoFilter{Status: "open"}, "2026-03-15")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range ts {
		got = append(got, x.Title+" "+x.Due)
	}
	return got
}

func weekly(t *testing.T, m todos, title, due string) int64 {
	t.Helper()
	id, err := m.Create(context.Background(), web.Todo{ColumnID: 1, Title: title, Due: due}, todoRepeat{Freq: "weekly", Interval: 1})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func complete(t *testing.T, m todos, id int64) int64 {
	t.Helper()
	next, err := m.Complete(context.Background(), id)
	if err != nil {
		t.Fatalf("Complete(%d): %v", id, err)
	}
	return next
}

func TestCompletingARecurringToDoOpensExactlyOneNextCopy(t *testing.T) {
	m, ctx := board(t), context.Background()
	id := weekly(t, m, "Bins", "2026-03-15")
	next := complete(t, m, id)
	if next == 0 {
		t.Fatal("no next copy")
	}
	if _, err := m.Complete(ctx, id); !errors.Is(err, errTodoCompleted) {
		t.Errorf("completing twice: %v, want errTodoCompleted", err)
	}
	if got := open(t, m); !slices.Equal(got, []string{"Bins 2026-03-22"}) {
		t.Errorf("open = %q", got)
	}
	plain, _ := m.Create(ctx, web.Todo{ColumnID: 1, Title: "Call", Due: "2026-03-10"}, todoRepeat{})
	if next := complete(t, m, plain); next != 0 {
		t.Errorf("a plain to-do made next copy %d", next)
	}
}

func TestReopeningRemovesTheOpenNextCopyOrIsRefused(t *testing.T) {
	m, ctx := board(t), context.Background()
	id := weekly(t, m, "Bins", "2026-03-15")
	next := complete(t, m, id)
	if removed, err := m.Reopen(ctx, id); err != nil || removed != next {
		t.Fatalf("Reopen = %d, %v; want %d", removed, err, next)
	}
	if got := open(t, m); !slices.Equal(got, []string{"Bins 2026-03-15"}) {
		t.Errorf("after reopening, open = %q", got)
	}
	next = complete(t, m, id)
	complete(t, m, next)
	if _, err := m.Reopen(ctx, id); !errors.Is(err, errNextCompleted) {
		t.Errorf("reopening behind a completed copy: %v, want errNextCompleted", err)
	}
	if got := open(t, m); !slices.Equal(got, []string{"Bins 2026-03-29"}) {
		t.Errorf("after the refusal, open = %q", got)
	}
}

func TestAChainedCopyCannotBeDeletedOrEdited(t *testing.T) {
	m, ctx := board(t), context.Background()
	id := weekly(t, m, "Bins", "2026-03-15")
	next := complete(t, m, id)
	if err := m.Delete(ctx, id); !errors.Is(err, errTodoChained) {
		t.Errorf("deleting a chained copy: %v, want errTodoChained", err)
	}
	if err := m.Edit(ctx, id, web.Todo{ColumnID: 1, Title: "Bins", Due: "2026-03-15"}, todoRepeat{Freq: "weekly", Interval: 1}); !errors.Is(err, errTodoCompleted) {
		t.Errorf("editing a completed copy: %v, want errTodoCompleted", err)
	}
	if err := m.Edit(ctx, next, web.Todo{ColumnID: 1, Title: "Recycling", Due: "2026-03-23"}, todoRepeat{Freq: "weekly", Interval: 1}); err != nil {
		t.Fatal(err)
	}
	if got := open(t, m); !slices.Equal(got, []string{"Recycling 2026-03-23"}) {
		t.Errorf("after the edit, open = %q", got)
	}
	if err := m.Delete(ctx, next); err != nil {
		t.Fatal(err)
	}
	if err := m.Delete(ctx, id); err != nil {
		t.Errorf("deleting once its next copy is gone: %v", err)
	}
	if got := open(t, m); len(got) != 0 {
		t.Errorf("open = %q", got)
	}
}

func TestOperationsOnAMissingToDo(t *testing.T) {
	m, ctx := board(t), context.Background()
	_, complete := m.Complete(ctx, 99)
	_, reopen := m.Reopen(ctx, 99)
	_, get := m.Get(ctx, 99)
	for name, err := range map[string]error{
		"Complete": complete, "Reopen": reopen, "Get": get, "Delete": m.Delete(ctx, 99),
		"Edit": m.Edit(ctx, 99, web.Todo{ColumnID: 1, Title: "x"}, todoRepeat{}),
	} {
		if !errors.Is(err, errTodoGone) {
			t.Errorf("%s: %v, want errTodoGone", name, err)
		}
	}
	if _, err := m.Create(ctx, web.Todo{ColumnID: 7, Title: "x"}, todoRepeat{}); !errors.Is(err, errNoColumn) {
		t.Errorf("Create in a missing column: %v, want errNoColumn", err)
	}
}

func TestGetDecodesTheRepeatForTheForm(t *testing.T) {
	m, ctx := board(t), context.Background()
	id, _ := m.Create(ctx, web.Todo{ColumnID: 1, Title: "Rent", Due: "2026-01-31", FromCompletion: true}, todoRepeat{Freq: "monthly", Interval: 2})
	got, err := m.Get(ctx, id)
	if err != nil || got.Repeat != "monthly" || got.Interval != "2" || !got.FromCompletion || got.Due != "2026-01-31" {
		t.Errorf("Get = %+v, %v", got, err)
	}
}

func TestTheDueFilter(t *testing.T) {
	m, ctx := board(t), context.Background()
	for _, due := range []string{"2026-03-14", "2026-03-15", "2026-03-16", ""} {
		if _, err := m.Create(ctx, web.Todo{ColumnID: 1, Title: "t" + due, Due: due}, todoRepeat{}); err != nil {
			t.Fatal(err)
		}
	}
	ts, err := m.List(ctx, todoFilter{Status: "open", To: "2026-03-15", ByDue: true}, "2026-03-15")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range ts {
		got = append(got, x.Due+" "+strconv.FormatBool(x.Overdue))
	}
	if want := []string{"2026-03-14 true", "2026-03-15 false"}; !slices.Equal(got, want) {
		t.Errorf("due today or overdue = %q, want %q", got, want)
	}
	if ts, _ := m.List(ctx, todoFilter{Status: "open", NoDue: true}, "2026-03-15"); len(ts) != 1 || ts[0].Due != "" {
		t.Errorf("no due date = %+v", ts)
	}
}
