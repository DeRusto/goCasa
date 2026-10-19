package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
)

var weekdayCodes = [7]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

// choreRound is one Round: its first and last day.
type choreRound struct{ start, end time.Time }

// roundOn is the Round of a Chore's schedule that day falls in: the day
// (daily, or a chosen weekday), its week or its calendar month. ok is false on
// a day a chosen-weekdays Chore doesn't run.
func roundOn(schedule, days string, day time.Time) (r choreRound, ok bool) {
	switch schedule {
	case "weekly":
		// shortcut: weeks start on Sunday, as on the calendar; follow week_start once household settings have it.
		s := dayAt(day, -int(day.Weekday()))
		return choreRound{s, dayAt(s, 6)}, true
	case "monthly":
		s := time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, day.Location())
		return choreRound{s, s.AddDate(0, 1, -1)}, true
	case "weekdays":
		if !strings.Contains(days, weekdayCodes[day.Weekday()]) {
			return r, false
		}
	}
	return choreRound{day, day}, true
}

// claimable is a Chore's Rounds that can still be completed on today, oldest
// first: the Round running today, if any, and the latest Round before it,
// which is Late until the Round after it ends; older ones are Missed. A Round
// that ended before the Chore was created never counts.
func claimable(schedule, days string, today, created time.Time) []choreRound {
	var rs []choreRound
	from := today
	if cur, ok := roundOn(schedule, days, today); ok {
		rs, from = append(rs, cur), cur.start
	}
	for i := 1; i <= 7; i++ {
		if r, ok := roundOn(schedule, days, dayAt(from, -i)); ok {
			if !r.end.Before(created) {
				rs = append([]choreRound{r}, rs...)
			}
			break
		}
	}
	return rs
}

// back is where a chore action returns to: Today when it came from there.
func back(r *http.Request) string {
	if r.PostFormValue("next") == "/today" {
		return "/today"
	}
	return "/chores"
}

// chores is every Chore with, for active ones, its claimable Rounds and who
// can still tap or has done each.
func (a *App) chores(ctx context.Context) ([]web.Chore, error) {
	loc := a.Config.Location
	today := dayAt(a.Now().In(loc), 0)
	rows, err := a.DB.QueryContext(ctx, "SELECT id, name, schedule, days, kind = 'anyone', points, archived, created_at FROM chores ORDER BY name, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cs []web.Chore
	created := map[int64]int64{}
	for rows.Next() {
		var c web.Chore
		var at int64
		if err := rows.Scan(&c.ID, &c.Name, &c.Schedule, &c.Days, &c.Anyone, &c.Points, &c.Archived, &at); err != nil {
			return nil, err
		}
		created[c.ID] = at
		cs = append(cs, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	assignees := map[int64][]web.Member{}
	err = a.eachRow(ctx, func(s func(...any) error) error {
		var chore int64
		var m web.Member
		if err := s(&chore, &m.ID, &m.Name, &m.Color, &m.HasAvatar); err != nil {
			return err
		}
		assignees[chore] = append(assignees[chore], m)
		return nil
	}, `SELECT ca.chore_id, m.id, m.name, m.color, m.avatar IS NOT NULL FROM chore_assignees ca JOIN members m ON m.id = ca.member_id
		WHERE NOT m.archived AND NOT m.pet ORDER BY m.name, m.id`)
	if err != nil {
		return nil, err
	}
	type key struct {
		chore int64
		round string
	}
	done := map[key][]web.ChoreTap{}
	err = a.eachRow(ctx, func(s func(...any) error) error {
		var k key
		var t web.ChoreTap
		if err := s(&t.CompletionID, &k.chore, &k.round, &t.Member.ID, &t.Member.Name, &t.Member.Color, &t.Member.HasAvatar); err != nil {
			return err
		}
		done[k] = append(done[k], t)
		return nil
	}, `SELECT c.id, c.chore_id, c.round_start, m.id, m.name, m.color, m.avatar IS NOT NULL FROM chore_completions c JOIN members m ON m.id = c.member_id
		WHERE c.round_start >= ? ORDER BY c.completed_at, c.id`, today.AddDate(0, -3, 0).Format(dateLayout))
	if err != nil {
		return nil, err
	}

	for i := range cs {
		c := &cs[i]
		if c.Archived {
			continue
		}
		for _, cr := range claimable(c.Schedule, c.Days, today, dayAt(time.Unix(created[c.ID], 0).In(loc), 0)) {
			rd := web.ChoreRound{Start: cr.start.Format(dateLayout), Late: cr.end.Before(today)}
			taps := done[key{c.ID, rd.Start}]
			open := false
			if !c.Anyone || len(taps) == 0 {
				// shortcut: an Each chore shows only its current assignees, so a completion by a since-unassigned Member isn't listed (its points still count).
				byMember := map[int64]web.ChoreTap{}
				for _, t := range taps {
					byMember[t.Member.ID] = t
				}
				taps = nil
				for _, m := range assignees[c.ID] {
					t, ok := byMember[m.ID]
					if !ok {
						t, open = web.ChoreTap{Member: m}, true
					}
					taps = append(taps, t)
				}
			}
			if rd.Late && !open {
				continue // nothing left to claim
			}
			rd.Taps = taps
			c.Rounds = append(c.Rounds, rd)
		}
	}
	return cs, nil
}

// eachRow runs query and calls f with each row's Scan.
func (a *App) eachRow(ctx context.Context, f func(scan func(...any) error) error, query string, args ...any) error {
	rows, err := a.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := f(rows.Scan); err != nil {
			return err
		}
	}
	return rows.Err()
}

// people is the active Members who can be assigned Chores.
func (a *App) people(r *http.Request) ([]web.Member, error) {
	ms, err := a.members(r, false)
	var ps []web.Member
	for _, m := range ms {
		if !m.Archived && !m.Pet {
			ps = append(ps, m)
		}
	}
	return ps, err
}

func (a *App) getChores(w http.ResponseWriter, r *http.Request) {
	cs, err := a.chores(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	ps, err := a.people(r)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	pts, err := a.points(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, web.ChoresBoard(cs, ps, pts))
}

// choreForm reads and checks the Chore form, or returns a message for the person.
func (a *App) choreForm(r *http.Request) (c web.Chore, assignees []int64, msg string) {
	c.Name = strings.TrimSpace(r.PostFormValue("name"))
	c.Schedule = r.PostFormValue("schedule")
	c.Anyone = r.PostFormValue("kind") == "anyone"
	var days []string
	for _, d := range web.Weekdays {
		if slices.Contains(r.PostForm["days"], d) {
			days = append(days, d)
		}
	}
	c.Days = strings.Join(days, ",")
	points, err := strconv.Atoi(r.PostFormValue("points"))
	c.Points = points
	assignees = parseIDs(r.PostForm["assignees"])
	switch {
	case c.Name == "" || len([]rune(c.Name)) > 100:
		return c, nil, "A Chore needs a name of 1 to 100 characters."
	case c.Schedule != "daily" && c.Schedule != "weekdays" && c.Schedule != "weekly" && c.Schedule != "monthly":
		return c, nil, "Pick how often it comes back."
	case c.Schedule == "weekdays" && len(days) == 0:
		return c, nil, "Pick the days of the week."
	case !slices.Contains([]string{"", "each", "anyone"}, r.PostFormValue("kind")):
		return c, nil, "Pick Each or Anyone."
	case err != nil || points < 0 || points > 10000:
		return c, nil, "Points are a whole number from 0 to 10000."
	case len(assignees) == 0:
		return c, nil, "Assign at least one person."
	}
	if c.Schedule != "weekdays" {
		c.Days = ""
	}
	ps, err := a.people(r)
	if err != nil {
		return c, nil, "Could not check the assignees."
	}
	ok := map[int64]bool{}
	for _, p := range ps {
		ok[p.ID] = true
	}
	for _, id := range assignees {
		if !ok[id] {
			return c, nil, "Chores are assigned to active people only, not pets."
		}
	}
	return c, assignees, ""
}

// saveChore creates a Chore, or updates the one in the path, with its assignees.
func (a *App) saveChore(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	c, assignees, msg := a.choreForm(r)
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	kind := "each"
	if c.Anyone {
		kind = "anyone"
	}
	tx, err := a.DB.BeginTx(r.Context(), nil)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	typ := "created"
	if r.PathValue("id") == "" {
		err = tx.QueryRowContext(r.Context(), "INSERT INTO chores (name, schedule, days, kind, points, created_at) VALUES (?, ?, ?, ?, ?, ?) RETURNING id",
			c.Name, c.Schedule, c.Days, kind, c.Points, a.Now().Unix()).Scan(&c.ID)
	} else {
		typ = "updated"
		c.ID, _ = strconv.ParseInt(r.PathValue("id"), 10, 64)
		err = tx.QueryRowContext(r.Context(), "UPDATE chores SET name = ?, schedule = ?, days = ?, kind = ?, points = ? WHERE id = ? RETURNING id",
			c.Name, c.Schedule, c.Days, kind, c.Points, c.ID).Scan(&c.ID)
		if err == nil {
			_, err = tx.ExecContext(r.Context(), "DELETE FROM chore_assignees WHERE chore_id = ?", c.ID)
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	for _, m := range assignees {
		if err == nil {
			_, err = tx.ExecContext(r.Context(), "INSERT OR IGNORE INTO chore_assignees (chore_id, member_id) VALUES (?, ?)", c.ID, m)
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.publish(typ, "chore", c.ID)
	http.Redirect(w, r, "/chores", http.StatusSeeOther)
}

func (a *App) getChore(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	c := web.Chore{ID: id, Assignees: map[int64]bool{}}
	err := a.DB.QueryRowContext(r.Context(), "SELECT name, schedule, days, kind = 'anyone', points, archived FROM chores WHERE id = ?", id).
		Scan(&c.Name, &c.Schedule, &c.Days, &c.Anyone, &c.Points, &c.Archived)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if err == nil {
		err = a.eachRow(r.Context(), func(s func(...any) error) error {
			var m int64
			err := s(&m)
			c.Assignees[m] = true
			return err
		}, "SELECT member_id FROM chore_assignees WHERE chore_id = ?", id)
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	ps, err := a.people(r)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, web.ChorePage(c, ps))
}

// setChoreArchived archives a Chore (off the board, its completions kept) or restores it.
func (a *App) setChoreArchived(archived bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.choreExec(w, r, "updated", "chore", "UPDATE chores SET archived = ? WHERE id = ?", archived)
	}
}

// choreExec runs query with args and the path's id last: 404 if no row
// changed, else it publishes the change and goes back.
func (a *App) choreExec(w http.ResponseWriter, r *http.Request, typ, entity, query string, args ...any) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	res, err := a.DB.ExecContext(r.Context(), query, append(args, id)...)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.NotFound(w, r)
		return
	}
	a.publish(typ, entity, id)
	http.Redirect(w, r, back(r), http.StatusSeeOther)
}

// completeChore credits the tapping Member with a claimable Round, once per
// Round per Member (once per Round for an Anyone chore), at the Chore's
// current points. Tapping a done Round again changes nothing.
func (a *App) completeChore(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	member, _ := strconv.ParseInt(r.PostFormValue("member_id"), 10, 64)
	round := r.PostFormValue("round")
	var schedule, days string
	var at int64
	err := a.DB.QueryRowContext(r.Context(), "SELECT schedule, days, created_at FROM chores WHERE id = ? AND NOT archived", id).Scan(&schedule, &days, &at)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	loc := a.Config.Location
	open := false
	for _, cr := range claimable(schedule, days, dayAt(a.Now().In(loc), 0), dayAt(time.Unix(at, 0).In(loc), 0)) {
		open = open || cr.start.Format(dateLayout) == round
	}
	if !open {
		http.Error(w, "That Round can't be claimed: it's Missed or hasn't started.", http.StatusConflict)
		return
	}
	res, err := a.DB.ExecContext(r.Context(), `INSERT INTO chore_completions (chore_id, member_id, round_start, completed_at, points_awarded)
		SELECT c.id, m.id, ?3, ?4, c.points FROM chores c
			JOIN chore_assignees ca ON ca.chore_id = c.id JOIN members m ON m.id = ca.member_id
		WHERE c.id = ?1 AND m.id = ?2 AND NOT m.archived AND NOT m.pet
			AND NOT EXISTS (SELECT 1 FROM chore_completions x WHERE x.chore_id = c.id AND x.round_start = ?3 AND (c.kind = 'anyone' OR x.member_id = m.id))`,
		id, member, round, a.Now().Unix())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var assigned bool
		if err := a.DB.QueryRowContext(r.Context(), `SELECT EXISTS (SELECT 1 FROM chore_assignees ca JOIN members m ON m.id = ca.member_id
			WHERE ca.chore_id = ? AND m.id = ? AND NOT m.archived AND NOT m.pet)`, id, member).Scan(&assigned); err != nil {
			a.serverError(w, r, err)
			return
		}
		if !assigned {
			http.Error(w, "Only someone assigned to this Chore can do it.", http.StatusBadRequest)
			return
		}
	} else {
		cid, _ := res.LastInsertId()
		a.publish("created", "chore_completion", cid)
	}
	http.Redirect(w, r, back(r), http.StatusSeeOther)
}

// undoCompletion is an Admin removing a completion, and so its points.
func (a *App) undoCompletion(w http.ResponseWriter, r *http.Request) {
	a.choreExec(w, r, "deleted", "chore_completion", "DELETE FROM chore_completions WHERE id = ?")
}
