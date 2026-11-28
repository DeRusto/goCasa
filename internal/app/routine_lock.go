package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
)

type routineTick struct {
	chore int64
	round string
}

// memberChoreGroups overlays provisional ticks on this Member's claimable
// Rounds. Today and the Chores board only use the underlying completions.
func (a *App) memberChoreGroups(ctx context.Context, cs []web.Chore, member int64, current, today string) ([]web.ChoreGroup, error) {
	ticks := map[routineTick]bool{}
	err := a.eachRow(ctx, func(scan func(...any) error) error {
		var t routineTick
		if err := scan(&t.chore, &t.round); err != nil {
			return err
		}
		ticks[t] = true
		return nil
	}, "SELECT chore_id, round_start FROM routine_ticks WHERE member_id = ? AND expires_on > ?", member, today)
	if err != nil {
		return nil, err
	}
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
	return choreGroups(cs, current), nil
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
	cs = memberChores(cs, member)
	i := slices.IndexFunc(cs, func(c web.Chore) bool { return c.ID == chore && c.Routine != "" })
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	c := cs[i]
	if acc.CanDo(web.LockChores) {
		a.completeChoreRound(w, r, chore, member, round, next)
		return
	}
	if !slices.ContainsFunc(c.Rounds, func(rd web.ChoreRound) bool { return rd.Start == round }) {
		http.Error(w, "That Round can't be claimed: it's Missed or hasn't started.", http.StatusConflict)
		return
	}
	// A Routine only holds daily or chosen-weekdays Each chores. As in
	// claimable, a Late Round expires when the next scheduled Round ends.
	hh, err := a.household(ctx)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	today := hh.Today(a.Now()).Format(dateLayout)
	expires := routineTickExpiry(c, round, hh)
	if expires <= today {
		http.Error(w, "That Round can't be claimed: it's Missed or hasn't started.", http.StatusConflict)
		return
	}
	res, err := a.exec(ctx, web.KindChore, `INSERT OR IGNORE INTO routine_ticks (member_id, chore_id, round_start, expires_on)
		SELECT ?1, c.id, ?3, ?5 FROM chores c
		JOIN chore_assignees ca ON ca.chore_id = c.id AND ca.member_id = ?1
		JOIN members m ON m.id = ca.member_id
		WHERE c.id = ?2 AND c.routine = ?4 AND NOT c.archived AND NOT m.archived AND NOT m.pet
		AND c.schedule = ?6 AND c.days = ?7
		AND NOT EXISTS (SELECT 1 FROM chore_completions x WHERE x.chore_id = c.id AND x.member_id = ?1 AND x.round_start = ?3)`,
		member, chore, round, c.Routine, expires, c.Schedule, c.Days)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		groups, err := a.memberChoreGroups(ctx, cs, member, "", today)
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

func routineTickExpiry(c web.Chore, round string, hh Household) string {
	start, err := time.ParseInLocation(dateLayout, round, hh.Zone)
	if err != nil {
		return ""
	}
	for offset := 1; offset <= 7; offset++ {
		if next, ok := roundOn(c.Schedule, c.Days, hh.WeekStart, dayAt(start, offset)); ok {
			return dayAt(next.end, 1).Format(dateLayout)
		}
	}
	return ""
}

func (a *App) pruneRoutineTicks(ctx context.Context, now time.Time) error {
	hh, err := a.household(ctx)
	if err != nil {
		return err
	}
	_, err = a.DB.ExecContext(ctx, "DELETE FROM routine_ticks WHERE expires_on <= ?", hh.Today(now).Format(dateLayout))
	return err
}

func routineConfirmPath(memberPage, routine string) string {
	return memberPage + "/routines/" + routine + "/confirm"
}

func (a *App) getRoutinePIN(w http.ResponseWriter, r *http.Request) {
	a.routinePINPage(w, r, http.StatusOK, "", nil)
}

func (a *App) routinePINPage(w http.ResponseWriter, r *http.Request, status int, message string, offered []string) {
	routine := r.PathValue("routine")
	member, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || !slices.Contains(web.Routines, routine) {
		http.NotFound(w, r)
		return
	}
	hh, err := a.household(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	var ticks []string
	err = a.eachRow(r.Context(), func(scan func(...any) error) error {
		var id string
		if err := scan(&id); err != nil {
			return err
		}
		ticks = append(ticks, id)
		return nil
	}, `SELECT t.id FROM routine_ticks t JOIN chores c ON c.id = t.chore_id
		WHERE t.member_id = ? AND c.routine = ? AND t.expires_on > ? ORDER BY t.id`,
		member, routine, hh.Today(a.Now()).Format(dateLayout))
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	next := fmt.Sprintf("/members/%d", member)
	if len(ticks) == 0 || offered != nil && !containsRoutineTicks(offered, ticks) {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	admins, err := a.members(r, true)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, status, web.RoutinePINPad(admins, message, routineConfirmPath(next, routine), next, strings.Join(ticks, ",")))
}

// containsRoutineTicks accepts a shrinking batch (a Round expired or was
// completed elsewhere), but a new tick needs a fresh prompt.
func containsRoutineTicks(offered, current []string) bool {
	for _, id := range current {
		if !slices.Contains(offered, id) {
			return false
		}
	}
	return true
}

var errStaleRoutinePIN = errors.New("Routine ticks changed after the PIN pad was opened")

func (a *App) confirmRoutine(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	routine := r.PathValue("routine")
	member, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || !slices.Contains(web.Routines, routine) {
		http.NotFound(w, r)
		return
	}
	next := fmt.Sprintf("/members/%d", member)
	batch := r.PostFormValue("ticks")
	if batch == "" {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	offered := strings.Split(batch, ",")
	admin, _ := strconv.ParseInt(r.PostFormValue("member_id"), 10, 64)
	result, err := a.checkPIN(ctx, admin, r.PostFormValue("pin"))
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	switch result {
	case pinBlocked:
		a.routinePINPage(w, r, http.StatusTooManyRequests, errPINBlocked, offered)
		return
	case pinNo:
		a.routinePINPage(w, r, http.StatusUnauthorized, "That PIN didn't match. Try again.", offered)
		return
	}
	hh, err := a.household(ctx)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	var changed bool
	err = a.inTxIf(ctx, &changed, func(tx *sql.Tx) error {
		now := a.Now()
		ticks, err := takeRoutineTicks(ctx, tx, member, routine, hh.Today(now).Format(dateLayout))
		if err != nil {
			return err
		}
		for id := range ticks {
			if !slices.Contains(offered, id) {
				return errStaleRoutinePIN
			}
		}
		for _, tick := range ticks {
			err := creditChoreRound(ctx, tx, tick.chore, member, tick.round, now.Unix())
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return err
			}
			changed = true
		}
		return nil
	}, web.KindChore, web.KindPoints)
	if err != nil && !errors.Is(err, errStaleRoutinePIN) {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// takeRoutineTicks starts with a write so two confirmations or an ordinary
// completion can't consume the same tick. A stale batch rolls back the delete.
func takeRoutineTicks(ctx context.Context, tx *sql.Tx, member int64, routine, today string) (map[string]routineTick, error) {
	rows, err := tx.QueryContext(ctx, `DELETE FROM routine_ticks WHERE member_id = ? AND expires_on > ?
		AND chore_id IN (SELECT id FROM chores WHERE routine = ?)
		RETURNING id, chore_id, round_start`, member, today, routine)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ticks := map[string]routineTick{}
	for rows.Next() {
		var id string
		var t routineTick
		if err := rows.Scan(&id, &t.chore, &t.round); err != nil {
			return nil, err
		}
		ticks[id] = t
	}
	return ticks, rows.Err()
}
