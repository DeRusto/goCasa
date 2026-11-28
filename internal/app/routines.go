package app

import (
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
)

// Routine is the current Routine at now under the slot times: "morning",
// "afternoon" or "evening". It only picks a Routine to show; a Chore's
// Rounds, Late and Missed never depend on it.
func (h Household) Routine(now time.Time) string {
	t := now.In(h.Zone)
	switch m := t.Hour()*60 + t.Minute(); {
	case m < h.MorningEnd:
		return "morning"
	case m < h.AfternoonEnd:
		return "afternoon"
	}
	return "evening"
}

// RoutineEnd is when the current Routine at now ends under the slot times:
// Evening ends at midnight, when Morning starts.
func (h Household) RoutineEnd(now time.Time) time.Time {
	t := now.In(h.Zone)
	at := func(min int) time.Time { return time.Date(t.Year(), t.Month(), t.Day(), 0, min, 0, 0, h.Zone) }
	switch h.Routine(now) {
	case "morning":
		return at(h.MorningEnd)
	case "afternoon":
		return at(h.AfternoonEnd)
	}
	return dayAt(t, 1)
}

// routineEligible is the SQL condition for a Chore that can be in a Routine:
// an active Each chore on a daily or chosen-weekdays schedule.
const routineEligible = "NOT archived AND kind = 'each' AND schedule IN ('daily', 'weekdays')"

// inRoutine reports whether a Chore saved with schedule and kind keeps its Routine.
func inRoutine(schedule string, anyone bool) bool {
	return !anyone && (schedule == "daily" || schedule == "weekdays")
}

var errNotEligible = errors.New("not a daily or chosen-weekdays Each chore")

// saveRoutines is the Routine builder's Save: each Routine's Chore ids in
// order (morning, afternoon, evening) and the eligible ones in none. Chores it
// doesn't list keep their place. It sits behind requireUnlock.
func (a *App) saveRoutines(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	seen := map[int64]bool{}
	type place struct {
		id       int64
		routine  *string
		position *int
	}
	var places []place
	for _, rt := range append(slices.Clone(web.Routines), "none") {
		for i, id := range parseIDs(r.PostForm[rt]) {
			if seen[id] {
				http.Error(w, "A Chore can be in one Routine only.", http.StatusBadRequest)
				return
			}
			seen[id] = true
			p := place{id: id}
			if rt != "none" {
				p.routine, p.position = &rt, &i
			}
			places = append(places, p)
		}
	}
	err := a.inTx(r.Context(), func(tx *sql.Tx) error {
		for _, p := range places {
			err := execOne(r.Context(), tx, "UPDATE chores SET routine = ?, routine_position = ? WHERE id = ? AND "+routineEligible, p.routine, p.position, p.id)
			if errors.Is(err, sql.ErrNoRows) {
				return errNotEligible
			} else if err != nil {
				return err
			}
		}
		return nil
	}, web.KindChore)
	switch {
	case errors.Is(err, errNotEligible):
		http.Error(w, "Only daily or chosen-weekdays Each chores can be in a Routine.", http.StatusBadRequest)
	case err != nil:
		a.serverError(w, r, err)
	default:
		http.Redirect(w, r, "/chores", http.StatusSeeOther)
	}
}

// slotTimes is the slot times sent as HH:MM, both nil when neither is sent;
// ok is false unless both are sent and Morning ends before Afternoon does.
func slotTimes(r *http.Request) (morningEnd, afternoonEnd *int, ok bool) {
	mv, av := r.PostFormValue("morning_end"), r.PostFormValue("afternoon_end")
	if mv == "" && av == "" {
		return nil, nil, true
	}
	m, err1 := time.Parse("15:04", strings.TrimSpace(mv))
	e, err2 := time.Parse("15:04", strings.TrimSpace(av))
	mins := func(t time.Time) int { return t.Hour()*60 + t.Minute() }
	if err1 != nil || err2 != nil || mins(m) == 0 || mins(m) >= mins(e) {
		return nil, nil, false
	}
	me, ae := mins(m), mins(e)
	return &me, &ae, true
}
