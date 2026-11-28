package app

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strconv"

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
	_, err = a.exec(ctx, web.KindChore, `INSERT OR IGNORE INTO routine_ticks (member_id, chore_id, round_start)
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
	http.Redirect(w, r, next, http.StatusSeeOther)
}
