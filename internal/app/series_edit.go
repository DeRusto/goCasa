package app

import "slices"

// editOutcome is what a change or deletion in an Edit scope does.
type editOutcome int

const (
	editSingle editOutcome = iota // not a Series: the event itself
	editOneOff                    // This event: a One-off edit of the Occurrence
	editWhole                     // the whole Series
	editSplit                     // This and following from a later Occurrence: a Split there
	editText                      // All events: the Series' text fields and Members only
)

// editPlan is how a change or deletion applies, decided by planEdit or planRemove.
type editPlan struct {
	outcome editOutcome
	changes editChanges // planEdit's only
}

// resets reports whether the plan drops the One-off edits from the Occurrence on.
func (p editPlan) resets() bool { return p.outcome == editWhole || p.outcome == editSplit }

// editChanges is what a change alters from the Occurrence as it was: each text
// field nil if unchanged, else its new value; whether its times changed; and
// whether its Members did.
type editChanges struct {
	title, location, description any
	retimed, members             bool
}

// membersOnly reports whether the change leaves the text and times alone.
func (ch editChanges) membersOnly() bool {
	return ch.title == nil && ch.location == nil && ch.description == nil && !ch.retimed
}

// planEdit decides how change c applies, in Edit scope sc, to an event that
// stands as was. first reports whether the Occurrence is its Series' first,
// and is called only for This and following. canRepeat is whether the event's
// calendar takes repeat changes; without it c.rerule is ignored. The changes'
// members compares c's and was' Members as given, so leave archived ones out of
// both where it is read. A change it won't make is a *refusal.
func planEdit(sc scope, inSeries bool, first func() (bool, error), c, was eventChange, canRepeat bool) (editPlan, error) {
	p := editPlan{changes: editChanges{
		retimed: c.allDay != was.allDay || shownTimes(c) != shownTimes(was),
		members: slices.ContainsFunc(c.members, func(m int64) bool { return !slices.Contains(was.members, m) }) ||
			slices.ContainsFunc(was.members, func(m int64) bool { return !slices.Contains(c.members, m) }),
	}}
	// Only the fields changed from the Occurrence apply, so an All events edit from a One-off edit doesn't spread its own changes.
	for _, f := range []struct {
		to       *any
		got, was string
	}{{&p.changes.title, c.title, was.title}, {&p.changes.location, c.location, was.location}, {&p.changes.description, c.description, was.description}} {
		if f.got != f.was {
			*f.to = f.got
		}
	}
	rerule := canRepeat && c.rerule
	switch {
	case !inSeries:
	case sc == scopeThis:
		if rerule {
			return editPlan{}, &refusal{msg: "A repeat change applies to This and following events."}
		}
		p.outcome = editOneOff
	case sc == scopeAll:
		if rerule || p.changes.retimed {
			what := "times"
			if canRepeat {
				what = "times or repeats"
			}
			return editPlan{}, &refusal{msg: "All events changes only the title, location, description and Members; change " + what + " with This event or This and following."}
		}
		p.outcome = editText
	case sc == scopeFollowing:
		var err error
		if p.outcome, err = following(first); err != nil {
			return editPlan{}, err
		}
	default:
		return editPlan{}, &refusal{msg: "Pick which events to change."}
	}
	return p, nil
}

// planRemove decides how a deletion in Edit scope sc applies, first as planEdit's.
func planRemove(sc scope, inSeries bool, first func() (bool, error)) (editPlan, error) {
	var p editPlan
	var err error
	switch {
	case !inSeries:
	case sc == scopeThis:
		p.outcome = editOneOff
	case sc == scopeAll:
		p.outcome = editWhole
	case sc == scopeFollowing:
		p.outcome, err = following(first)
	default:
		err = &refusal{msg: "Pick which events to delete."}
	}
	return p, err
}

// following is This and following's outcome: from the first Occurrence it means the whole Series.
func following(first func() (bool, error)) (editOutcome, error) {
	f, err := first()
	if f {
		return editWhole, err
	}
	return editSplit, err
}
