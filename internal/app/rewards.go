package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/DeRusto/goCasa/internal/web"
)

// points is the board's points part: balances, Rewards and pending Redemptions.
func (a *App) points(ctx context.Context) (web.Points, error) {
	p := web.Points{Balances: map[int64]int{}}
	err := a.eachRow(ctx, func(s func(...any) error) error {
		var m int64
		var n int
		err := s(&m, &n)
		p.Balances[m] = n
		return err
	}, "SELECT member_id, points FROM balances")
	if err == nil {
		err = a.eachRow(ctx, func(s func(...any) error) error {
			var rw web.Reward
			err := s(&rw.ID, &rw.Name, &rw.Cost, &rw.Archived)
			p.Rewards = append(p.Rewards, rw)
			return err
		}, "SELECT id, name, points_cost, archived FROM rewards ORDER BY points_cost, name, id")
	}
	if err == nil {
		err = a.eachRow(ctx, func(s func(...any) error) error {
			var rd web.Redemption
			err := s(&rd.ID, &rd.Reward.Name, &rd.Reward.Cost, &rd.Member.ID, &rd.Member.Name, &rd.Member.Color, &rd.Member.HasAvatar)
			p.Pending = append(p.Pending, rd)
			return err
		}, `SELECT rd.id, rw.name, rw.points_cost, m.id, m.name, m.color, m.avatar IS NOT NULL
			FROM redemptions rd JOIN rewards rw ON rw.id = rd.reward_id JOIN members m ON m.id = rd.member_id
			WHERE rd.status = 'pending' AND NOT m.archived ORDER BY rd.requested_at, rd.id`)
	}
	return p, err
}

// person is the form's member_id if it's an active person (not a pet), else 0.
func (a *App) person(r *http.Request) (int64, error) {
	id, _ := strconv.ParseInt(r.PostFormValue("member_id"), 10, 64)
	var ok bool
	err := a.DB.QueryRowContext(r.Context(), "SELECT EXISTS (SELECT 1 FROM members WHERE id = ? AND NOT archived AND NOT pet)", id).Scan(&ok)
	if !ok {
		id = 0
	}
	return id, err
}

// createReward adds a Reward. Its cost is fixed: Rewards are never edited, so
// approved Redemptions keep counting what they cost.
func (a *App) createReward(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	cost, err := strconv.Atoi(r.PostFormValue("points"))
	if name == "" || len([]rune(name)) > 100 || err != nil || cost < 1 || cost > 100000 {
		http.Error(w, "A Reward needs a name of 1 to 100 characters and a cost from 1 to 100000 points.", http.StatusBadRequest)
		return
	}
	if _, err := a.exec(r.Context(), web.KindPoints, "INSERT INTO rewards (name, points_cost) VALUES (?, ?)", name, cost); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/chores", http.StatusSeeOther)
}

// setRewardArchived archives a Reward (off the board, its Redemptions kept) or restores it.
func (a *App) setRewardArchived(archived bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.choreExec(w, r, []web.Kind{web.KindPoints}, "UPDATE rewards SET archived = ? WHERE id = ?", archived)
	}
}

// redeem is a Member asking for a Reward they can afford; nothing is spent
// until an Admin approves.
func (a *App) redeem(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	member, err := a.person(r)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if member == 0 {
		http.Error(w, "Only an active person can ask for a Reward.", http.StatusBadRequest)
		return
	}
	var affordable bool
	err = a.DB.QueryRowContext(r.Context(), `SELECT b.points >= rw.points_cost FROM rewards rw, balances b
		WHERE rw.id = ? AND NOT rw.archived AND b.member_id = ?`, id, member).Scan(&affordable)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	if !affordable {
		http.Error(w, "Not enough points for that Reward yet.", http.StatusConflict)
		return
	}
	if _, err := a.exec(r.Context(), web.KindPoints, "INSERT INTO redemptions (reward_id, member_id, requested_at) VALUES (?, ?, ?)",
		id, member, a.Now().Unix()); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/chores", http.StatusSeeOther)
}

// decide is an Admin approving or declining a pending Redemption. Approving
// can't take the balance below zero or be for an archived Member; the check and the change are one statement.
func (a *App) decide(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		res, err := a.exec(r.Context(), web.KindPoints, `UPDATE redemptions SET status = ?1, decided_at = ?2, decided_by = ?3
			WHERE id = ?4 AND status = 'pending' AND (?1 = 'declined' OR (SELECT NOT archived FROM members WHERE id = redemptions.member_id) AND (SELECT b.points - rw.points_cost >= 0
				FROM balances b, rewards rw WHERE b.member_id = redemptions.member_id AND rw.id = redemptions.reward_id))`,
			status, a.Now().Unix(), unlockedAdmin(r.Context()), id)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var pending bool
			if err := a.DB.QueryRowContext(r.Context(), "SELECT EXISTS (SELECT 1 FROM redemptions WHERE id = ? AND status = 'pending')", id).Scan(&pending); err != nil {
				a.serverError(w, r, err)
				return
			}
			if !pending {
				http.NotFound(w, r)
				return
			}
			http.Error(w, "Approving that would take the balance below zero, or they're archived.", http.StatusConflict)
			return
		}
		http.Redirect(w, r, "/chores", http.StatusSeeOther)
	}
}

// adjust is an Admin adding or removing points by hand, with a reason: the
// only way a balance can go below zero.
func (a *App) adjust(w http.ResponseWriter, r *http.Request) {
	member, err := a.person(r)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	points, perr := strconv.Atoi(r.PostFormValue("points"))
	reason := strings.TrimSpace(r.PostFormValue("reason"))
	switch {
	case member == 0:
		http.Error(w, "Pick an active person.", http.StatusBadRequest)
		return
	case perr != nil || points == 0 || points < -100000 || points > 100000:
		http.Error(w, "Points are a whole number from -100000 to 100000, not 0.", http.StatusBadRequest)
		return
	case reason == "" || len([]rune(reason)) > 200:
		http.Error(w, "An Adjustment needs a reason of 1 to 200 characters.", http.StatusBadRequest)
		return
	}
	if _, err := a.exec(r.Context(), web.KindPoints, "INSERT INTO point_adjustments (member_id, points, reason, created_at, created_by) VALUES (?, ?, ?, ?, ?)",
		member, points, reason, a.Now().Unix(), unlockedAdmin(r.Context())); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/chores", http.StatusSeeOther)
}
