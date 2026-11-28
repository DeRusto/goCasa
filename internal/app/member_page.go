package app

import (
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strconv"

	"github.com/DeRusto/goCasa/internal/web"
)

// memberPagePath is a Member page's URL: chore and Reward actions may return there.
var memberPagePath = regexp.MustCompile(`^/members/[0-9]+$`)

// getMemberPage shows one active Member's today in their style: the weather,
// their own and Household events (with a peek at tomorrow in Standard style),
// their Chores, their columns' to-dos and their points. Archived, deleted, unknown
// and pet Members get a plain page saying it isn't available.
func (a *App) getMemberPage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := web.MemberPage{}
	p.Member.ID, _ = strconv.ParseInt(r.PathValue("id"), 10, 64)
	err := a.DB.QueryRowContext(ctx, "SELECT name, color, avatar IS NOT NULL, style FROM members WHERE id = ? AND NOT archived AND NOT pet", p.Member.ID).
		Scan(&p.Member.Name, &p.Member.Color, &p.Member.HasAvatar, &p.Member.Style)
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
	kid := p.Member.Style == "kid"
	clock := hh.Clock()
	if kid {
		clock = plainClock(hh.Clock24)
	}
	p.Day = web.TodayPage{Now: now, Clock: now.Format(clock), Clock24: hh.Clock24, NoAmPm: kid, MidnightIn: secondsTo(now, dayAt(today, 1))}
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
	if kid && p.Day.Weather != nil {
		if _, f, err := a.cachedForecast(ctx); err != nil {
			a.Log.Error("reading cached weather", "err", err)
		} else if f != nil {
			p.Weather = weatherNow(hh, f, today.Format(dateLayout), clock)
		}
	}

	cs, err := a.chores(ctx)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	p.Groups, err = a.memberChoreGroups(ctx, memberChores(cs, p.Member.ID), p.Member.ID, hh.Routine(now), today.Format(dateLayout))
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	p.RoutineIn = secondsTo(now, hh.RoutineEnd(now))

	if p.Todos, p.MoreTodos, err = (todos{a}).ForMember(ctx, p.Member.ID, today.Format(dateLayout)); err != nil {
		a.serverError(w, r, err)
		return
	}

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
	if kid {
		a.render(w, r, http.StatusOK, web.KidMemberPage(p))
		return
	}
	a.render(w, r, http.StatusOK, web.MemberPageView(p))
}

// plainClock is Kid style's time layout: the Household's 12/24-hour setting,
// with no am/pm in 12-hour ("3:30").
func plainClock(h24 bool) string {
	if h24 {
		return "15:04"
	}
	return "3:04"
}

// choreGroups files a Member's Chores under the three Routines, each in its
// Routine's order, then Other chores (the rest, in the order given), marking
// the current Routine.
func choreGroups(cs []web.Chore, current string) []web.ChoreGroup {
	var gs []web.ChoreGroup
	for _, rt := range append(slices.Clone(web.Routines), "other") {
		g := web.ChoreGroup{Routine: rt, Current: rt == current}
		for _, c := range cs {
			if c.Routine == rt || rt == "other" && c.Routine == "" {
				g.Chores = append(g.Chores, c)
			}
		}
		slices.SortStableFunc(g.Chores, func(a, b web.Chore) int { return a.RoutinePosition - b.RoutinePosition })
		gs = append(gs, g)
	}
	return gs
}

// memberChores is Member id's share of today's Chores: the active ones they're
// assigned with a Round to show, each Round keeping only their own tap, or an
// Anyone chore's completion by whoever did it. Like Today, a Late Round stays,
// done or not, while anyone can still claim it.
func memberChores(cs []web.Chore, member int64) []web.Chore {
	var out []web.Chore
	for _, c := range cs {
		if c.Archived || !c.Assignees[member] {
			continue
		}
		var rounds []web.ChoreRound
		for _, rd := range c.Rounds {
			var taps []web.ChoreTap
			for _, t := range rd.Taps {
				if t.Member.ID == member || c.Anyone && t.CompletionID != 0 {
					taps = append(taps, t)
				}
			}
			if len(taps) > 0 {
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
