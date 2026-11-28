package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
)

type routineTick struct {
	chore int64
	round string
}

// memberChoreGroups overlays provisional ticks on this Member's claimable
// Rounds. Today and the Chores board only use the underlying completions.
func (a *App) memberChoreGroups(ctx context.Context, cs []web.Chore, member int64, current string) ([]web.ChoreGroup, error) {
	ticks := map[routineTick]bool{}
	err := a.eachRow(ctx, func(scan func(...any) error) error {
		var t routineTick
		if err := scan(&t.chore, &t.round); err != nil {
			return err
		}
		ticks[t] = true
		return nil
	}, "SELECT chore_id, round_start FROM routine_ticks WHERE member_id = ?", member)
	if err != nil {
		return nil, err
	}
	return choreGroups(withRoutineTicks(memberChores(cs, member), ticks), current), nil
}

func withRoutineTicks(cs []web.Chore, ticks map[routineTick]bool) []web.Chore {
	for _, c := range cs {
		if c.Routine == "" {
			continue
		}
		for _, rd := range c.Rounds {
			for i := range rd.Taps {
				t := &rd.Taps[i]
				t.Provisional = t.CompletionID == 0 && ticks[routineTick{c.ID, rd.Start}]
			}
		}
	}
	return cs
}

func (a *App) tickRoutineChore(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	member, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	chore, _ := strconv.ParseInt(r.PathValue("chore"), 10, 64)
	round := r.PostFormValue("round")
	next := fmt.Sprintf("/members/%d", member)
	acc, err := a.access(ctx)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	cs, err := a.chores(ctx)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	memberCS := memberChores(cs, member)
	i := slices.IndexFunc(memberCS, func(c web.Chore) bool { return c.ID == chore && c.Routine != "" })
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	c := memberCS[i]
	j := slices.IndexFunc(c.Rounds, func(rd web.ChoreRound) bool { return rd.Start == round })
	if j < 0 {
		http.Error(w, "That Round can't be claimed: it's Missed or hasn't started.", http.StatusConflict)
		return
	}
	if acc.CanDo(web.LockChores) {
		// Use the normal completion, with the Member bound to the page's URL.
		r.SetPathValue("id", strconv.FormatInt(chore, 10))
		r.PostForm.Set("member_id", strconv.FormatInt(member, 10))
		r.PostForm.Set("next", next)
		a.completeChore(w, r)
		return
	}
	res, err := a.exec(ctx, web.KindChore, `INSERT OR IGNORE INTO routine_ticks (member_id, chore_id, round_start)
		SELECT ?1, c.id, ?3 FROM chores c
		JOIN chore_assignees ca ON ca.chore_id = c.id AND ca.member_id = ?1
		JOIN members m ON m.id = ca.member_id
		WHERE c.id = ?2 AND c.routine = ?4 AND NOT c.archived AND NOT m.archived AND NOT m.pet
		AND NOT EXISTS (SELECT 1 FROM chore_completions x WHERE x.chore_id = c.id AND x.member_id = ?1 AND x.round_start = ?3)`,
		member, chore, round, c.Routine)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		// Read the saved state before prompting. A retry of the same tick
		// can still use Confirm on the page.
		cs, err = a.chores(ctx)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		groups, err := a.memberChoreGroups(ctx, cs, member, "")
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		for _, g := range groups {
			if g.Routine == c.Routine && g.Pending() {
				if done, total := g.Progress(); done == total {
					next = routineConfirmPath(next, g.Routine)
				}
				break
			}
		}
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

func routineConfirmPath(memberPage, routine string) string {
	return memberPage + "/routines/" + routine + "/confirm"
}

func (a *App) getRoutinePIN(w http.ResponseWriter, r *http.Request) {
	a.routinePINPage(w, r, http.StatusOK, "")
}

func (a *App) routinePINPage(w http.ResponseWriter, r *http.Request, status int, message string) {
	routine := r.PathValue("routine")
	member, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || !slices.Contains(web.Routines, routine) {
		http.NotFound(w, r)
		return
	}
	admins, err := a.members(r, true)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	next := fmt.Sprintf("/members/%d", member)
	a.render(w, r, status, web.RoutinePINPad(admins, message, routineConfirmPath(next, routine), next))
}

func (a *App) confirmRoutine(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	routine := r.PathValue("routine")
	member, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || !slices.Contains(web.Routines, routine) {
		http.NotFound(w, r)
		return
	}
	admin, _ := strconv.ParseInt(r.PostFormValue("member_id"), 10, 64)
	result, err := a.checkPIN(ctx, admin, r.PostFormValue("pin"))
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	switch result {
	case pinBlocked:
		a.routinePINPage(w, r, http.StatusTooManyRequests, errPINBlocked)
		return
	case pinNo:
		a.routinePINPage(w, r, http.StatusUnauthorized, "That PIN didn't match. Try again.")
		return
	}
	hh, err := a.household(ctx)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	var changed bool
	err = a.inTxIf(ctx, &changed, func(tx *sql.Tx) error {
		ticks, err := takeRoutineTicks(ctx, tx, member, routine)
		if err != nil {
			return err
		}
		now := a.Now()
		for _, tick := range ticks {
			var schedule, days string
			var created int64
			if err := tx.QueryRowContext(ctx, "SELECT schedule, days, created_at FROM chores WHERE id = ?", tick.chore).
				Scan(&schedule, &days, &created); err != nil {
				return err
			}
			rounds := claimable(schedule, days, hh.WeekStart, hh.Today(now), hh.Today(time.Unix(created, 0)))
			if !slices.ContainsFunc(rounds, func(rd choreRound) bool { return rd.start.Format(dateLayout) == tick.round }) {
				continue // Missed, or the schedule changed.
			}
			err := creditChoreRound(ctx, tx, tick.chore, member, tick.round, now.Unix())
			if errors.Is(err, sql.ErrNoRows) {
				continue // Already completed, or the Member is no longer assigned.
			}
			if err != nil {
				return err
			}
			changed = true
		}
		return nil
	}, web.KindChore, web.KindPoints)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/members/%d", member), http.StatusSeeOther)
}

// takeRoutineTicks starts the transaction with a write so a simultaneous
// confirmation or ordinary completion can't consume the same tick. A failed
// credit rolls back the whole batch, including this deletion.
func takeRoutineTicks(ctx context.Context, tx *sql.Tx, member int64, routine string) ([]routineTick, error) {
	rows, err := tx.QueryContext(ctx, `DELETE FROM routine_ticks WHERE member_id = ?
		AND chore_id IN (SELECT id FROM chores WHERE routine = ? AND NOT archived)
		RETURNING chore_id, round_start`, member, routine)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ticks []routineTick
	for rows.Next() {
		var t routineTick
		if err := rows.Scan(&t.chore, &t.round); err != nil {
			return nil, err
		}
		ticks = append(ticks, t)
	}
	return ticks, rows.Err()
}
