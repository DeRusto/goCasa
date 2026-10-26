package app

import (
	"errors"
	"slices"
	"testing"
)

// firstIs is a first func answering first, and a count of its calls.
func firstIs(first bool) (func() (bool, error), *int) {
	n := new(int)
	return func() (bool, error) { *n++; return first, nil }, n
}

func TestPlanEdit(t *testing.T) {
	was := timedAt("Walk", at(16, 9), 1, 1)
	retimed := timedAt("Walk", at(16, 10), 1, 1)
	renamed := timedAt("Hike", at(16, 9), 1, 1)
	rerule := repeat(was, "FREQ=WEEKLY", nil, nil)
	allRefusal := "All events changes only the title, location, description and Members; change times or repeats with This event or This and following."
	for _, tc := range []struct {
		name      string
		sc        scope
		inSeries  bool
		first     bool
		c         eventChange
		canRepeat bool
		want      editOutcome // when not refused
		refusal   string
		asked     bool // whether first is called, once
	}{
		{name: "not a Series", sc: scopeFollowing, c: retimed, canRepeat: true, want: editSingle},
		{name: "not a Series, no scope", c: rerule, canRepeat: true, want: editSingle},
		{name: "This event", sc: scopeThis, inSeries: true, c: retimed, canRepeat: true, want: editOneOff},
		{name: "This event, repeat changed", sc: scopeThis, inSeries: true, c: rerule, canRepeat: true, refusal: "A repeat change applies to This and following events."},
		{name: "This event, repeat changed, can't repeat", sc: scopeThis, inSeries: true, c: rerule, want: editOneOff},
		{name: "All events", sc: scopeAll, inSeries: true, c: renamed, canRepeat: true, want: editText},
		{name: "All events, retimed", sc: scopeAll, inSeries: true, c: retimed, canRepeat: true, refusal: allRefusal},
		{name: "All events, retimed, can't repeat", sc: scopeAll, inSeries: true, c: retimed,
			refusal: "All events changes only the title, location, description and Members; change times with This event or This and following."},
		{name: "All events, repeat changed", sc: scopeAll, inSeries: true, c: rerule, canRepeat: true, refusal: allRefusal},
		{name: "All events, repeat changed, can't repeat", sc: scopeAll, inSeries: true, c: rerule, want: editText},
		{name: "This and following, first Occurrence", sc: scopeFollowing, inSeries: true, first: true, c: retimed, canRepeat: true, want: editWhole, asked: true},
		{name: "This and following, later Occurrence", sc: scopeFollowing, inSeries: true, c: retimed, canRepeat: true, want: editSplit, asked: true},
		{name: "This and following, repeat changed", sc: scopeFollowing, inSeries: true, c: rerule, canRepeat: true, want: editSplit, asked: true},
		{name: "no scope", inSeries: true, c: renamed, canRepeat: true, refusal: "Pick which events to change."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, asked := firstIs(tc.first)
			p, err := planEdit(tc.sc, tc.inSeries, first, tc.c, was, tc.canRepeat)
			if want := map[bool]int{true: 1}[tc.asked]; *asked != want {
				t.Errorf("first called %d times", *asked)
			}
			if tc.refusal != "" {
				if r := refused(t, err); r.msg != tc.refusal {
					t.Errorf("refused %q", r.msg)
				}
				return
			}
			if err != nil || p.outcome != tc.want {
				t.Errorf("planEdit = %v, %v; want %v", p.outcome, err, tc.want)
			}
			if want := tc.want == editWhole || tc.want == editSplit; p.resets() != want {
				t.Errorf("resets = %v", p.resets())
			}
		})
	}
}

func TestPlanEditFirstFails(t *testing.T) {
	boom := errors.New("boom")
	_, err := planEdit(scopeFollowing, true, func() (bool, error) { return false, boom }, timedAt("Walk", at(16, 9), 1), timedAt("Walk", at(16, 9), 1), true)
	if !errors.Is(err, boom) {
		t.Errorf("planEdit = %v", err)
	}
}

func TestPlanEditChanges(t *testing.T) {
	was := timedAt("Walk", at(16, 9), 1, 1, 2)
	was.location, was.description = "Park", "Bring water"
	for _, tc := range []struct {
		name string
		c    func(eventChange) eventChange
		want editChanges
	}{
		{name: "nothing", c: func(c eventChange) eventChange { return c }},
		{name: "title", c: func(c eventChange) eventChange { c.title = "Hike"; return c }, want: editChanges{title: "Hike"}},
		{name: "location and description", c: func(c eventChange) eventChange { c.location, c.description = "", "Snacks"; return c },
			want: editChanges{location: "", description: "Snacks"}},
		{name: "end moved", c: func(c eventChange) eventChange { c.end = c.end.(int64) + 3600; return c }, want: editChanges{retimed: true}},
		{name: "made all-day", c: func(eventChange) eventChange {
			c := allDayOn("Walk", "2026-03-16")
			c.location, c.description, c.members = "Park", "Bring water", []int64{1, 2}
			return c
		},
			want: editChanges{retimed: true}},
		{name: "a Member dropped", c: func(c eventChange) eventChange { c.members = []int64{1}; return c }, want: editChanges{members: true}},
		{name: "Members reordered", c: func(c eventChange) eventChange { c.members = []int64{2, 1}; return c }},
		{name: "a repeat change alone", c: func(c eventChange) eventChange { return repeat(c, "FREQ=WEEKLY", nil, nil) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.c(was)
			c.members = slices.Clone(c.members)
			p, err := planEdit(scopeThis, false, nil, c, was, true)
			if err != nil || p.changes != tc.want {
				t.Errorf("changes = %+v, %v; want %+v", p.changes, err, tc.want)
			}
			if got, want := p.changes.membersOnly(), tc.want == (editChanges{}) || tc.want == (editChanges{members: true}); got != want {
				t.Errorf("membersOnly = %v", got)
			}
		})
	}
}

func TestPlanRemove(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sc       scope
		inSeries bool
		first    bool
		want     editOutcome
		refusal  string
		asked    bool
	}{
		{name: "not a Series", sc: scopeAll, want: editSingle},
		{name: "This event", sc: scopeThis, inSeries: true, want: editOneOff},
		{name: "All events", sc: scopeAll, inSeries: true, want: editWhole},
		{name: "This and following, first Occurrence", sc: scopeFollowing, inSeries: true, first: true, want: editWhole, asked: true},
		{name: "This and following, later Occurrence", sc: scopeFollowing, inSeries: true, want: editSplit, asked: true},
		{name: "no scope", inSeries: true, refusal: "Pick which events to delete."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first, asked := firstIs(tc.first)
			p, err := planRemove(tc.sc, tc.inSeries, first)
			if want := map[bool]int{true: 1}[tc.asked]; *asked != want {
				t.Errorf("first called %d times", *asked)
			}
			if tc.refusal != "" {
				if r := refused(t, err); r.msg != tc.refusal {
					t.Errorf("refused %q", r.msg)
				}
				return
			}
			if err != nil || p.outcome != tc.want {
				t.Errorf("planRemove = %v, %v; want %v", p.outcome, err, tc.want)
			}
		})
	}
}
