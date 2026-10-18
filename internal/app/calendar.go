package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
)

const (
	dateLayout  = "2006-01-02"
	localLayout = "2006-01-02T15:04" // <input type="datetime-local">
	agendaDays  = 30
)

// localCalendars is the ids of the calendars GoCasa owns; localCalendar limits a query on events to them.
const (
	localCalendars = "SELECT c.id FROM source_calendars c JOIN integrations i ON i.id = c.integration_id WHERE i.type = 'local'"
	localCalendar  = "calendar_id IN (" + localCalendars + ")"
)

// dayAt is local midnight i days after day; time.Date keeps it midnight across DST.
func dayAt(day time.Time, i int) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day()+i, 0, 0, 0, 0, day.Location())
}

// parseLocal reads a datetime-local value in zone. An ambiguous fall-back time
// takes its first offset (spec §7.1); Go alone may pick either.
func parseLocal(v string, zone *time.Location) (time.Time, error) {
	t, err := time.ParseInLocation(localLayout, v, zone)
	if err != nil {
		return t, err
	}
	for _, d := range []time.Duration{2 * time.Hour, time.Hour, 30 * time.Minute} { // largest first: the earliest match
		if e := t.Add(-d); e.Format(localLayout) == t.Format(localLayout) {
			return e, nil
		}
	}
	return t, nil
}

// parseIDs reads Member ids given as "1,2" and/or repeated values.
func parseIDs(vs []string) []int64 {
	var ids []int64
	for _, v := range vs {
		for _, s := range strings.Split(v, ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err == nil {
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// span is the days a view shows around date, and the dates its arrows go to.
func span(view string, date time.Time) (first time.Time, n int, prev, next time.Time) {
	// shortcut: weeks start on Sunday; follow week_start once household settings exist.
	weekStart := func(d time.Time) time.Time { return dayAt(d, -int(d.Weekday())) }
	switch view {
	case "day":
		return date, 1, dayAt(date, -1), dayAt(date, 1)
	case "week":
		return weekStart(date), 7, dayAt(date, -7), dayAt(date, 7)
	case "agenda":
		return date, agendaDays, dayAt(date, -agendaDays), dayAt(date, agendaDays)
	}
	m1 := dayAt(date, 1-date.Day())
	first = weekStart(m1)
	last := dayAt(m1.AddDate(0, 1, 0), -1)
	n = (int(last.Sub(first).Hours()/24+0.5)/7 + 1) * 7
	return first, n, m1.AddDate(0, -1, 0), m1.AddDate(0, 1, 0)
}

type calendarEvent struct {
	id            int64
	title         string
	allDay        bool
	start, end    int64  // timed
	date, endDate string // all-day
	calendarColor string
	memberColors  string
}

func (a *App) getCalendar(w http.ResponseWriter, r *http.Request) {
	loc := a.Config.Location
	q := r.URL.Query()
	view := q.Get("view")
	if view != "week" && view != "day" && view != "agenda" {
		view = "month"
	}
	today := dayAt(a.Now().In(loc), 0)
	date, err := time.ParseInLocation(dateLayout, q.Get("date"), loc)
	if err != nil {
		date = today
	}
	users := parseIDs(q["users"])
	first, n, prev, next := span(view, date)
	end := dayAt(first, n)

	filter := ""
	if len(users) > 0 {
		b, _ := json.Marshal(users)
		filter = string(b)
	}
	rows, err := a.DB.QueryContext(r.Context(), `SELECT e.id, e.title, e.all_day, coalesce(e.start_utc, 0), coalesce(e.end_utc, 0),
			coalesce(e.all_day_date, ''), coalesce(e.all_day_end_date, ''), c.color,
			coalesce((SELECT group_concat(m.color, ',' ORDER BY m.name, m.id) FROM event_members em JOIN members m ON m.id = em.member_id WHERE em.event_id = e.id), '')
		FROM events e JOIN source_calendars c ON c.id = e.calendar_id
		WHERE (NOT e.all_day AND e.start_utc < ?2 AND e.end_utc > ?1 OR e.all_day AND e.all_day_date < ?4 AND e.all_day_end_date > ?3)
			AND (?5 = '' OR NOT EXISTS (SELECT 1 FROM event_members WHERE event_id = e.id)
				OR EXISTS (SELECT 1 FROM event_members WHERE event_id = e.id AND member_id IN (SELECT value FROM json_each(?5))))
		ORDER BY e.all_day DESC, e.start_utc, e.all_day_date, e.title, e.id`,
		first.Unix(), end.Unix(), first.Format(dateLayout), end.Format(dateLayout), filter)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	var evs []calendarEvent
	for rows.Next() {
		var e calendarEvent
		if err := rows.Scan(&e.id, &e.title, &e.allDay, &e.start, &e.end, &e.date, &e.endDate, &e.calendarColor, &e.memberColors); err != nil {
			rows.Close()
			a.serverError(w, r, err)
			return
		}
		evs = append(evs, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		a.serverError(w, r, err)
		return
	}

	p := web.CalendarPage{View: view, Date: date, Users: map[int64]bool{}}
	for _, id := range users {
		p.Users[id] = true
	}
	for i := range n {
		d0, d1 := dayAt(first, i), dayAt(first, i+1)
		ds := d0.Format(dateLayout)
		day := web.CalendarDay{Date: d0, Today: d0.Equal(today), Faded: view == "month" && d0.Month() != date.Month()}
		for _, e := range evs {
			ev := web.CalendarEvent{ID: e.id, Title: e.title, Colors: []string{e.calendarColor}}
			if e.memberColors != "" {
				ev.Colors = strings.Split(e.memberColors, ",")
			}
			if e.allDay {
				if e.date > ds || e.endDate <= ds {
					continue
				}
				ev.Time = "All day"
			} else {
				if e.start >= d1.Unix() || e.end <= d0.Unix() {
					continue
				}
				from, to := "…", "…"
				if e.start >= d0.Unix() {
					from = time.Unix(e.start, 0).In(loc).Format("15:04")
				}
				if e.end <= d1.Unix() {
					to = time.Unix(e.end, 0).In(loc).Format("15:04")
				}
				ev.Time = from + "–" + to
			}
			day.Events = append(day.Events, ev)
		}
		if view != "agenda" || len(day.Events) > 0 {
			p.Days = append(p.Days, day)
		}
	}
	link := func(v string, d time.Time) string {
		lq := url.Values{"view": {v}, "date": {d.Format(dateLayout)}}
		if len(users) > 0 {
			ss := make([]string, len(users))
			for i, id := range users {
				ss[i] = strconv.FormatInt(id, 10)
			}
			lq.Set("users", strings.Join(ss, ","))
		}
		return "/calendar?" + lq.Encode()
	}
	p.Prev, p.Next, p.Today = link(view, prev), link(view, next), link(view, today)
	p.ViewLinks = map[string]string{}
	for _, v := range []string{"month", "week", "day", "agenda"} {
		p.ViewLinks[v] = link(v, date)
	}
	if p.Members, err = a.activeMembers(r); err != nil {
		a.serverError(w, r, err)
		return
	}
	if p.Calendars, err = a.calendars(r); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, web.Calendar(p))
}

func (a *App) activeMembers(r *http.Request) ([]web.Member, error) {
	ms, err := a.members(r, false)
	active := ms[:0]
	for _, m := range ms {
		if !m.Archived {
			active = append(active, m)
		}
	}
	return active, err
}

func (a *App) calendars(r *http.Request) ([]web.CalendarInfo, error) {
	rows, err := a.DB.QueryContext(r.Context(), "SELECT id, name, color FROM source_calendars WHERE id IN ("+localCalendars+") ORDER BY name, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cs []web.CalendarInfo
	for rows.Next() {
		var c web.CalendarInfo
		if err := rows.Scan(&c.ID, &c.Name, &c.Color); err != nil {
			return nil, err
		}
		cs = append(cs, c)
	}
	return cs, rows.Err()
}

func (a *App) createCalendar(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	color := strings.ToLower(r.PostFormValue("color"))
	if name == "" || len([]rune(name)) > 50 || !colorRE.MatchString(color) {
		http.Error(w, "A calendar needs a name of 1 to 50 characters and a colour.", http.StatusBadRequest)
		return
	}
	if _, err := a.DB.ExecContext(r.Context(), "INSERT INTO source_calendars (integration_id, name, color) VALUES ((SELECT id FROM integrations WHERE type = 'local'), ?, ?)", name, color); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/calendar", http.StatusSeeOther)
}

// eventRow is a validated event form in its stored form.
type eventRow struct {
	calendarID                      int64
	title, location, description    string
	allDay                          bool
	start, end, tzid, date, endDate any // NULL for the other kind of event
	members                         []int64
	firstDay                        string // in the Household's zone, to show after saving
}

func (a *App) parseEventForm(r *http.Request) (web.EventForm, eventRow, string) {
	r.ParseForm()
	f := web.EventForm{
		Title:       strings.TrimSpace(r.PostFormValue("title")),
		Location:    strings.TrimSpace(r.PostFormValue("location")),
		Description: strings.TrimSpace(r.PostFormValue("description")),
		AllDay:      r.PostFormValue("all_day") != "",
		Start:       r.PostFormValue("start"),
		End:         r.PostFormValue("end"),
		TZ:          strings.TrimSpace(r.PostFormValue("tz")),
		StartDate:   r.PostFormValue("start_date"),
		EndDate:     r.PostFormValue("end_date"),
		Members:     map[int64]bool{},
	}
	f.CalendarID, _ = strconv.ParseInt(r.PostFormValue("calendar_id"), 10, 64)
	e := eventRow{calendarID: f.CalendarID, title: f.Title, location: f.Location, description: f.Description, allDay: f.AllDay, members: parseIDs(r.PostForm["members"])}
	for _, id := range e.members {
		f.Members[id] = true
	}
	switch {
	case f.Title == "" || len([]rune(f.Title)) > 200:
		return f, e, "A title is 1 to 200 characters."
	case len([]rune(f.Location)) > 200:
		return f, e, "A location is at most 200 characters."
	case len([]rune(f.Description)) > 5000:
		return f, e, "A description is at most 5000 characters."
	}
	if f.AllDay {
		d0, err0 := time.Parse(dateLayout, f.StartDate)
		d1, err1 := time.Parse(dateLayout, f.EndDate)
		if err0 != nil || err1 != nil || d1.Before(d0) {
			return f, e, "Pick a first and last day, the last not before the first."
		}
		e.date, e.endDate, e.firstDay = f.StartDate, d1.AddDate(0, 0, 1).Format(dateLayout), f.StartDate
		return f, e, ""
	}
	zone, err := time.LoadLocation(f.TZ)
	if err != nil || f.TZ == "" || f.TZ == "Local" {
		return f, e, "Pick a time zone, like Europe/London."
	}
	t0, err0 := parseLocal(f.Start, zone)
	t1, err1 := parseLocal(f.End, zone)
	if err0 != nil || err1 != nil || !t1.After(t0) {
		return f, e, "Pick a start and an end after it."
	}
	// Go moves a time in a spring-forward gap; refuse it rather than save another time.
	if t0.Format(localLayout) != f.Start || t1.Format(localLayout) != f.End {
		return f, e, "That time doesn't exist in " + f.TZ + ": the clocks change then."
	}
	e.start, e.end, e.tzid = t0.Unix(), t1.Unix(), f.TZ
	e.firstDay = t0.In(a.Config.Location).Format(dateLayout)
	return f, e, ""
}

func (a *App) eventPage(w http.ResponseWriter, r *http.Request, status int, f web.EventForm, errMsg string) {
	cals, err := a.calendars(r)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	ms, err := a.activeMembers(r)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, status, web.EventPage(f, cals, ms, errMsg))
}

func (a *App) newEvent(w http.ResponseWriter, r *http.Request) {
	loc := a.Config.Location
	date, err := time.ParseInLocation(dateLayout, r.URL.Query().Get("date"), loc)
	if err != nil {
		date = dayAt(a.Now().In(loc), 0)
	}
	d := date.Format(dateLayout)
	a.eventPage(w, r, http.StatusOK, web.EventForm{
		Start: d + "T09:00", End: d + "T10:00", TZ: loc.String(), StartDate: d, EndDate: d, Members: map[int64]bool{},
	}, "")
}

func (a *App) getEvent(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	f := web.EventForm{ID: id, Members: map[int64]bool{}}
	var start, end sql.NullInt64
	var tzid, date, endDate sql.NullString
	err := a.DB.QueryRowContext(r.Context(), "SELECT calendar_id, title, location, description, all_day, start_utc, end_utc, start_tzid, all_day_date, all_day_end_date FROM events WHERE id = ? AND "+localCalendar, id).
		Scan(&f.CalendarID, &f.Title, &f.Location, &f.Description, &f.AllDay, &start, &end, &tzid, &date, &endDate)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	if f.AllDay {
		last, _ := time.Parse(dateLayout, endDate.String)
		f.StartDate, f.EndDate = date.String, last.AddDate(0, 0, -1).Format(dateLayout)
		f.TZ = a.Config.Location.String()
		f.Start, f.End = f.StartDate+"T09:00", f.StartDate+"T10:00"
	} else {
		zone, err := time.LoadLocation(tzid.String)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		f.TZ = tzid.String
		t0, t1 := time.Unix(start.Int64, 0).In(zone), time.Unix(end.Int64, 0).In(zone)
		f.Start, f.End = t0.Format(localLayout), t1.Format(localLayout)
		f.StartDate, f.EndDate = t0.Format(dateLayout), t0.Format(dateLayout)
	}
	rows, err := a.DB.QueryContext(r.Context(), "SELECT member_id FROM event_members WHERE event_id = ?", id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	for rows.Next() {
		var m int64
		if err := rows.Scan(&m); err != nil {
			rows.Close()
			a.serverError(w, r, err)
			return
		}
		f.Members[m] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.eventPage(w, r, http.StatusOK, f, "")
}

// saveEvent creates (id 0) or updates a local event and its Members.
func (a *App) saveEvent(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if r.PathValue("id") != "" && id <= 0 {
		http.NotFound(w, r)
		return
	}
	f, e, msg := a.parseEventForm(r)
	f.ID = id
	if msg == "" {
		var ok bool
		if err := a.DB.QueryRowContext(r.Context(), "SELECT ? IN ("+localCalendars+")", e.calendarID).Scan(&ok); err != nil {
			a.serverError(w, r, err)
			return
		}
		if !ok {
			msg = "Pick a calendar."
		}
	}
	if msg != "" {
		a.eventPage(w, r, http.StatusBadRequest, f, msg)
		return
	}
	tx, err := a.DB.BeginTx(r.Context(), nil)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	now := a.Now().Unix()
	if id == 0 {
		err = tx.QueryRowContext(r.Context(), `INSERT INTO events (calendar_id, title, location, description, all_day, start_utc, end_utc, start_tzid, all_day_date, all_day_end_date, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
			e.calendarID, e.title, e.location, e.description, e.allDay, e.start, e.end, e.tzid, e.date, e.endDate, now).Scan(&id)
	} else {
		err = tx.QueryRowContext(r.Context(), `UPDATE events SET calendar_id = ?, title = ?, location = ?, description = ?, all_day = ?,
				start_utc = ?, end_utc = ?, start_tzid = ?, all_day_date = ?, all_day_end_date = ?, updated_at = ?
			WHERE id = ? AND `+localCalendar+" RETURNING id",
			e.calendarID, e.title, e.location, e.description, e.allDay, e.start, e.end, e.tzid, e.date, e.endDate, now, id).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	// Archived Members are hidden from the picker, so they stay on the event.
	if _, err := tx.ExecContext(r.Context(), "DELETE FROM event_members WHERE event_id = ? AND member_id IN (SELECT id FROM members WHERE NOT archived)", id); err != nil {
		a.serverError(w, r, err)
		return
	}
	for _, m := range e.members {
		if _, err := tx.ExecContext(r.Context(), "INSERT OR IGNORE INTO event_members (event_id, member_id) SELECT ?, id FROM members WHERE id = ? AND NOT archived", id, m); err != nil {
			a.serverError(w, r, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/calendar?date="+e.firstDay, http.StatusSeeOther)
}

func (a *App) deleteEvent(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	res, err := a.DB.ExecContext(r.Context(), "DELETE FROM events WHERE id = ? AND "+localCalendar, id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.NotFound(w, r)
		return
	}
	http.Redirect(w, r, "/calendar", http.StatusSeeOther)
}
