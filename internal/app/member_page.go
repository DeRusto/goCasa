package app

import (
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strconv"

	"github.com/DeRusto/goCasa/internal/web"
)

// memberPagePath is a Member page's URL: chore and Reward actions may return there.
var memberPagePath = regexp.MustCompile(`^/members/[0-9]+$`)

// getMemberPage shows one active Member's today, in Standard style: the
// weather, their own and Household events with a peek at tomorrow, their
// Chores and their points. Archived, deleted, unknown and pet Members get a
// plain page saying it isn't available.
func (a *App) getMemberPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := web.MemberPage{}
	p.Member.ID, _ = strconv.ParseInt(r.PathValue("id"), 10, 64)
	err := a.DB.QueryRowContext(ctx, "SELECT name, color, avatar IS NOT NULL FROM members WHERE id = ? AND NOT archived AND NOT pet", p.Member.ID).
		Scan(&p.Member.Name, &p.Member.Color, &p.Member.HasAvatar)
	if errors.Is(err, sql.ErrNoRows) {
		a.render(w, r, http.StatusNotFound, web.MemberUnavailable())
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}

	now := a.Now().In(a.location(ctx))
	today := dayAt(now, 0)
	hh, err := a.household(ctx)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	clock := hh.Clock()
	p.Day = web.TodayPage{Now: now, Clock: now.Format(clock), Clock24: hh.Clock24, MidnightIn: secondsTo(now, dayAt(today, 1))}
	evs, err := a.eventsIn(ctx, today, dayAt(today, 2), []int64{p.Member.ID})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	for i := range p.Day.Days {
		p.Day.Days[i] = calendarDay(evs, dayAt(today, i), today, now.Unix(), clock)
	}
	if p.Day.Weather, err = a.weatherHeader(ctx); err != nil {
		a.Log.Error("reading cached weather", "err", err) // the page still renders, without weather
	}

	cs, err := a.chores(ctx)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	p.Chores = memberChores(cs, p.Member.ID)

	pts, err := a.points(ctx)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	p.Balance = pts.Balances[p.Member.ID]
	for _, rw := range pts.Rewards {
		if !rw.Archived {
			p.Rewards = append(p.Rewards, rw)
		}
	}
	for _, rd := range pts.Pending {
		if rd.Member.ID == p.Member.ID {
			p.Waiting = append(p.Waiting, rd)
		}
	}
	a.render(w, r, http.StatusOK, web.MemberPageView(p))
}

// memberChores is Member id's share of today's Chores: the active ones they're
// assigned with a Round to show, each Round keeping only their own tap, or an
// Anyone chore's completion by whoever did it. A Late Round shows only while
// they can still claim it.
func memberChores(cs []web.Chore, member int64) []web.Chore {
	var out []web.Chore
	for _, c := range cs {
		if c.Archived || !c.Assignees[member] {
			continue
		}
		var rounds []web.ChoreRound
		for _, rd := range c.Rounds {
			var taps []web.ChoreTap
			open := false
			for _, t := range rd.Taps {
				if t.Member.ID == member || c.Anyone && t.CompletionID != 0 {
					taps = append(taps, t)
					open = open || t.CompletionID == 0
				}
			}
			if len(taps) > 0 && (open || !rd.Late) {
				rd.Taps = taps
				rounds = append(rounds, rd)
			}
		}
		if len(rounds) > 0 {
			c.Rounds = rounds
			out = append(out, c)
		}
	}
	return out
}
