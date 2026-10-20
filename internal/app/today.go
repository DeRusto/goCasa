package app

import (
	"net/http"

	"github.com/DeRusto/goCasa/internal/web"
)

// getToday shows the Today page: the weather header, today's and tomorrow's
// agenda for everyone, today's Chores, and open to-dos due today or overdue.
func (a *App) getToday(w http.ResponseWriter, r *http.Request) {
	now := a.Now().In(a.location(r.Context()))
	today := dayAt(now, 0)
	hh, err := a.household(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	clock := hh.Clock()
	p := web.TodayPage{Now: now, Clock: now.Format(clock), Clock24: hh.Clock24, MidnightIn: secondsTo(now, dayAt(today, 1))}
	evs, err := a.eventsIn(r.Context(), today, dayAt(today, 2), nil)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	for i := range p.Days {
		p.Days[i] = calendarDay(evs, dayAt(today, i), today, now.Unix(), clock)
	}
	if p.Weather, err = a.weatherHeader(r.Context()); err != nil {
		a.Log.Error("reading cached weather", "err", err) // the page still renders, without weather
	}
	cs, err := a.chores(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	for _, c := range cs {
		if len(c.Rounds) > 0 {
			p.Chores = append(p.Chores, c)
		}
	}
	ds := today.Format(dateLayout)
	if p.Todos, err = (todos{a}).List(r.Context(), todoFilter{Status: "open", To: ds, ByDue: true}, ds); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, web.Today(p))
}
