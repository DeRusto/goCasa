package app

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
	"github.com/teambition/rrule-go"
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
	id            int64 // a Series' own for its Occurrences
	original      int64 // an Occurrence's key (see expand); 0 for a single event
	title         string
	allDay        bool
	start, end    int64  // timed
	date, endDate string // all-day
	calendarColor string
	memberColors  string
	shown         bool // passes the Member filter
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
	// Single events in the window, plus every Series that starts before its end or has a One-off edit in it, with all its One-off edits.
	rows, err := a.DB.QueryContext(r.Context(), `SELECT e.id, e.title, e.all_day, coalesce(e.start_utc, 0), coalesce(e.end_utc, 0),
			coalesce(e.all_day_date, ''), coalesce(e.all_day_end_date, ''), c.color,
			coalesce((SELECT group_concat(m.color, ',' ORDER BY m.name, m.id) FROM event_members em JOIN members m ON m.id = em.member_id WHERE em.event_id = e.id), ''),
			?5 = '' OR NOT EXISTS (SELECT 1 FROM event_members WHERE event_id = e.id)
				OR EXISTS (SELECT 1 FROM event_members WHERE event_id = e.id AND member_id IN (SELECT value FROM json_each(?5))),
			coalesce(e.start_tzid, ''), coalesce(e.rrule, ''), e.exdates_json, e.rdates_json,
			coalesce(e.recurring_master_id, 0), coalesce(e.original_start_utc, 0), e.status = 'cancelled'
		FROM events e JOIN source_calendars c ON c.id = e.calendar_id
		WHERE e.rrule IS NULL AND e.recurring_master_id IS NULL
				AND (NOT e.all_day AND e.start_utc < ?2 AND e.end_utc > ?1 OR e.all_day AND e.all_day_date < ?4 AND e.all_day_end_date > ?3)
			OR coalesce(e.recurring_master_id, e.id) IN (SELECT id FROM events WHERE rrule IS NOT NULL AND coalesce(start_utc < ?2, all_day_date < ?4))
			OR coalesce(e.recurring_master_id, e.id) IN (SELECT recurring_master_id FROM events WHERE recurring_master_id IS NOT NULL
				AND (NOT all_day AND start_utc < ?2 AND end_utc > ?1 OR all_day AND all_day_date < ?4 AND all_day_end_date > ?3))
		ORDER BY e.recurring_master_id IS NOT NULL`, // masters before their edits
		first.Unix(), end.Unix(), first.Format(dateLayout), end.Format(dateLayout), filter)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	var evs []calendarEvent
	var masters []*storedSeries
	byID := map[int64]*storedSeries{}
	for rows.Next() {
		var e calendarEvent
		var tzid, ex, rd string
		var s series
		var o oneOff
		var masterID int64
		if err := rows.Scan(&e.id, &e.title, &e.allDay, &e.start, &e.end, &e.date, &e.endDate, &e.calendarColor, &e.memberColors, &e.shown,
			&tzid, &s.rule, &ex, &rd, &masterID, &o.original, &o.cancelled); err != nil {
			rows.Close()
			a.serverError(w, r, err)
			return
		}
		switch {
		case masterID != 0:
			if m := byID[masterID]; m != nil {
				o.ev = e
				m.oneOffs = append(m.oneOffs, o)
			}
		case s.rule != "":
			ss := &storedSeries{ev: e, s: s}
			err := errors.Join(json.Unmarshal([]byte(ex), &ss.s.exdates), json.Unmarshal([]byte(rd), &ss.s.rdates))
			if !e.allDay && err == nil {
				ss.zone, err = time.LoadLocation(tzid)
			}
			if err != nil {
				rows.Close()
				a.serverError(w, r, err)
				return
			}
			masters = append(masters, ss)
			byID[e.id] = ss
		default:
			evs = append(evs, e)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		a.serverError(w, r, err)
		return
	}
	for _, m := range masters {
		occs, err := expand(m.ev, m.s, m.zone, m.oneOffs, first, end)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		evs = append(evs, occs...)
	}
	evs = slices.DeleteFunc(evs, func(e calendarEvent) bool { return !e.shown })
	slices.SortFunc(evs, func(x, y calendarEvent) int {
		if x.allDay != y.allDay {
			if x.allDay {
				return -1
			}
			return 1
		}
		return cmp.Or(cmp.Compare(x.start, y.start), strings.Compare(x.date, y.date), strings.Compare(x.title, y.title), cmp.Compare(x.id, y.id))
	})

	p := web.CalendarPage{View: view, Date: date, Users: map[int64]bool{}}
	for _, id := range users {
		p.Users[id] = true
	}
	for i := range n {
		d0, d1 := dayAt(first, i), dayAt(first, i+1)
		ds := d0.Format(dateLayout)
		day := web.CalendarDay{Date: d0, Today: d0.Equal(today), Faded: view == "month" && d0.Month() != date.Month()}
		for _, e := range evs {
			ev := web.CalendarEvent{ID: e.id, Original: e.original, Title: e.title, Colors: []string{e.calendarColor}}
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
	var id int64
	if err := a.DB.QueryRowContext(r.Context(), "INSERT INTO source_calendars (integration_id, name, color) VALUES ((SELECT id FROM integrations WHERE type = 'local'), ?, ?) RETURNING id", name, color).Scan(&id); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.publish("created", "calendar", id)
	http.Redirect(w, r, "/calendar", http.StatusSeeOther)
}

// eventRow is a validated event form in its stored form.
type eventRow struct {
	calendarID                      int64
	title, location, description    string
	allDay                          bool
	start, end, tzid, date, endDate any // NULL for the other kind of event
	rrule                           any // NULL unless it repeats
	exdates, rdates                 string
	members                         []int64
	firstDay                        string // in the Household's zone, to show after saving
}

var weekdays = map[string]bool{"MO": true, "TU": true, "WE": true, "TH": true, "FR": true, "SA": true, "SU": true}

// parseRepeat reads the dialog's repeat fields into a rule and its EXDATEs and
// RDATEs (as stored), or a message for the person.
func parseRepeat(f *web.EventForm, r *http.Request) (rule string, exdates, rdates []string, msg string) {
	f.Repeat, f.Interval, f.Custom = r.PostFormValue("repeat"), r.PostFormValue("interval"), strings.TrimSpace(r.PostFormValue("rrule"))
	f.ByDay = map[string]bool{}
	switch f.Repeat {
	case "":
		return "", nil, nil, ""
	case "daily", "weekly", "monthly", "yearly":
		n := 1
		if f.Interval != "" {
			var err error
			if n, err = strconv.Atoi(f.Interval); err != nil || n < 1 || n > 999 {
				return "", nil, nil, "Repeat every 1 to 999 days, weeks, months or years."
			}
		}
		rule = "FREQ=" + strings.ToUpper(f.Repeat) + ";INTERVAL=" + strconv.Itoa(n)
		if f.Repeat == "weekly" && len(r.PostForm["byday"]) > 0 {
			for _, d := range r.PostForm["byday"] {
				if !weekdays[d] {
					return "", nil, nil, "Pick days of the week."
				}
				f.ByDay[d] = true
			}
			rule += ";BYDAY=" + strings.Join(r.PostForm["byday"], ",")
		}
	case "custom":
		layout, basic := localLayout, "20060102T150405"
		if f.AllDay {
			layout, basic = dateLayout, "20060102"
		}
		for _, line := range strings.Split(f.Custom, "\n") {
			name, value, _ := strings.Cut(strings.TrimSpace(line), ":")
			switch name {
			case "":
			case "RRULE":
				rule = value
			case "EXDATE", "RDATE":
				for _, v := range strings.Split(value, ",") {
					t, err := time.Parse(basic, v)
					if err != nil {
						return "", nil, nil, "Write " + name + " dates like " + basic + "."
					}
					if name == "EXDATE" {
						exdates = append(exdates, t.Format(layout))
					} else {
						rdates = append(rdates, t.Format(layout))
					}
				}
			default:
				return "", nil, nil, "Use RRULE, EXDATE and RDATE lines."
			}
		}
	default:
		return "", nil, nil, "Pick how it repeats."
	}
	opt, err := rrule.StrToROption(rule)
	if err == nil {
		_, err = rrule.NewRRule(*opt)
	}
	if err != nil || rule == "" {
		return "", nil, nil, "That repeat rule isn't one GoCasa understands."
	}
	if opt.Freq > rrule.DAILY || len(opt.Byhour)+len(opt.Byminute)+len(opt.Bysecond) > 0 {
		return "", nil, nil, "A Series repeats at most daily."
	}
	return rule, exdates, rdates, ""
}

// ruleText is a stored rule as the advanced field writes it.
func ruleText(s series) string {
	basic := strings.NewReplacer("-", "", ":", "")
	text := "RRULE:" + s.rule
	for _, l := range []struct {
		name string
		ds   []string
	}{{"EXDATE", s.exdates}, {"RDATE", s.rdates}} {
		if len(l.ds) == 0 {
			continue
		}
		vs := make([]string, len(l.ds))
		for i, d := range l.ds {
			vs[i] = basic.Replace(d)
			if len(d) == len(localLayout) {
				vs[i] += "00"
			}
		}
		text += "\n" + l.name + ":" + strings.Join(vs, ",")
	}
	return text
}

// parseEventForm validates the dialog; withRepeat reads its repeat fields too,
// which a This event edit doesn't have.
func (a *App) parseEventForm(r *http.Request, withRepeat bool) (web.EventForm, eventRow, string) {
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
		ByDay:       map[string]bool{},
	}
	f.CalendarID, _ = strconv.ParseInt(r.PostFormValue("calendar_id"), 10, 64)
	e := eventRow{calendarID: f.CalendarID, title: f.Title, location: f.Location, description: f.Description, allDay: f.AllDay, members: parseIDs(r.PostForm["members"]), exdates: "[]", rdates: "[]"}
	for _, id := range e.members {
		f.Members[id] = true
	}
	// Read the repeat first so any error page still shows it.
	rule, ex, rd, repeatMsg := "", []string(nil), []string(nil), ""
	if withRepeat {
		rule, ex, rd, repeatMsg = parseRepeat(&f, r)
	}
	switch {
	case f.Title == "" || len([]rune(f.Title)) > 200:
		return f, e, "A title is 1 to 200 characters."
	case len([]rune(f.Location)) > 200:
		return f, e, "A location is at most 200 characters."
	case len([]rune(f.Description)) > 5000:
		return f, e, "A description is at most 5000 characters."
	}
	if repeatMsg != "" {
		return f, e, repeatMsg
	}
	if rule != "" {
		b1, _ := json.Marshal(append([]string{}, ex...))
		b2, _ := json.Marshal(append([]string{}, rd...))
		e.rrule, e.exdates, e.rdates = rule, string(b1), string(b2)
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
		Start: d + "T09:00", End: d + "T10:00", TZ: loc.String(), StartDate: d, EndDate: d, Members: map[int64]bool{}, ByDay: map[string]bool{},
	}, "")
}

// storedSeries is a local event that isn't a One-off edit; s.rule is "" for a single event.
type storedSeries struct {
	ev         calendarEvent
	calendarID int64
	zone       *time.Location // nil when all-day
	s          series
	oneOffs    []oneOff
}

// loadEvent reads local event id, or sql.ErrNoRows if it is missing or a One-off edit.
func (a *App) loadEvent(ctx context.Context, id int64) (storedSeries, error) {
	ss := storedSeries{ev: calendarEvent{id: id}}
	var tzid, date, endDate sql.NullString
	var start, end sql.NullInt64
	var ex, rd string
	err := a.DB.QueryRowContext(ctx, `SELECT calendar_id, title, all_day, start_utc, end_utc, start_tzid, all_day_date, all_day_end_date, coalesce(rrule, ''), exdates_json, rdates_json
		FROM events WHERE id = ? AND recurring_master_id IS NULL AND `+localCalendar, id).
		Scan(&ss.calendarID, &ss.ev.title, &ss.ev.allDay, &start, &end, &tzid, &date, &endDate, &ss.s.rule, &ex, &rd)
	if err != nil {
		return ss, err
	}
	ss.ev.start, ss.ev.end, ss.ev.date, ss.ev.endDate = start.Int64, end.Int64, date.String, endDate.String
	if err := errors.Join(json.Unmarshal([]byte(ex), &ss.s.exdates), json.Unmarshal([]byte(rd), &ss.s.rdates)); err != nil {
		return ss, err
	}
	if !ss.ev.allDay {
		ss.zone, err = time.LoadLocation(tzid.String)
	}
	return ss, err
}

// occurrenceAt finds the Occurrence of Series ss whose key is key ("" is the
// first): as the Series makes it, and its One-off edit row's id (0 if none).
// ok is false if the Series has no such Occurrence or it was deleted.
func (a *App) occurrenceAt(ctx context.Context, ss storedSeries, key string) (occ calendarEvent, editID int64, ok bool, err error) {
	original := ss.ev.start
	if ss.ev.allDay {
		d, _ := time.Parse(dateLayout, ss.ev.date)
		original = d.Unix()
	}
	from := time.Unix(original, 0).UTC()
	if key == "" { // the first Occurrence, which is not DTSTART when e.g. BYDAY leaves its weekday out
		occs, err := expand(ss.ev, ss.s, ss.zone, nil, from, from.AddDate(horizonYears, 0, 0))
		if err != nil || len(occs) == 0 {
			return occ, 0, false, err
		}
		original = occs[0].original
	} else if original, err = strconv.ParseInt(key, 10, 64); err != nil {
		return occ, 0, false, nil
	}
	var status string
	err = a.DB.QueryRowContext(ctx, "SELECT id, status FROM events WHERE recurring_master_id = ? AND original_start_utc = ?", ss.ev.id, original).Scan(&editID, &status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) || status == "cancelled" {
		return occ, 0, false, err
	}
	from = time.Unix(original, 0).UTC()
	occs, err := expand(ss.ev, ss.s, ss.zone, nil, from, from.Add(24*time.Hour))
	if err != nil {
		return occ, 0, false, err
	}
	for _, o := range occs {
		if o.original == original {
			return o, editID, true, nil
		}
	}
	occ.original = original
	return occ, editID, editID != 0, nil
}

// eventForm is stored event id (a single event, master or One-off edit row) as the dialog shows it.
func (a *App) eventForm(ctx context.Context, id int64) (web.EventForm, error) {
	f := web.EventForm{ID: id, Members: map[int64]bool{}, ByDay: map[string]bool{}}
	var start, end sql.NullInt64
	var tzid, date, endDate sql.NullString
	err := a.DB.QueryRowContext(ctx, "SELECT calendar_id, title, location, description, all_day, start_utc, end_utc, start_tzid, all_day_date, all_day_end_date FROM events WHERE id = ?", id).
		Scan(&f.CalendarID, &f.Title, &f.Location, &f.Description, &f.AllDay, &start, &end, &tzid, &date, &endDate)
	if err != nil {
		return f, err
	}
	if f.AllDay {
		last, _ := time.Parse(dateLayout, endDate.String)
		f.StartDate, f.EndDate = date.String, last.AddDate(0, 0, -1).Format(dateLayout)
		f.TZ = a.Config.Location.String()
		f.Start, f.End = f.StartDate+"T09:00", f.StartDate+"T10:00"
	} else {
		zone, err := time.LoadLocation(tzid.String)
		if err != nil {
			return f, err
		}
		f.TZ = tzid.String
		t0, t1 := time.Unix(start.Int64, 0).In(zone), time.Unix(end.Int64, 0).In(zone)
		f.Start, f.End = t0.Format(localLayout), t1.Format(localLayout)
		f.StartDate, f.EndDate = t0.Format(dateLayout), t0.Format(dateLayout)
	}
	rows, err := a.DB.QueryContext(ctx, "SELECT member_id FROM event_members WHERE event_id = ?", id)
	if err != nil {
		return f, err
	}
	defer rows.Close()
	for rows.Next() {
		var m int64
		if err := rows.Scan(&m); err != nil {
			return f, err
		}
		f.Members[m] = true
	}
	return f, rows.Err()
}

// occurrenceForm is the dialog for Occurrence key of Series ss: its One-off
// edit if it has one, else the Series' own fields at its time.
func (a *App) occurrenceForm(ctx context.Context, ss storedSeries, key string) (f web.EventForm, occ calendarEvent, editID int64, ok bool, err error) {
	if occ, editID, ok, err = a.occurrenceAt(ctx, ss, key); err != nil || !ok {
		return f, occ, editID, ok, err
	}
	if f, err = a.eventForm(ctx, cmp.Or(editID, ss.ev.id)); err != nil {
		return f, occ, editID, ok, err
	}
	if editID == 0 {
		if occ.allDay {
			last, _ := time.Parse(dateLayout, occ.endDate)
			f.StartDate, f.EndDate = occ.date, last.AddDate(0, 0, -1).Format(dateLayout)
		} else {
			t0, t1 := time.Unix(occ.start, 0).In(ss.zone), time.Unix(occ.end, 0).In(ss.zone)
			f.Start, f.End, f.StartDate, f.EndDate = t0.Format(localLayout), t1.Format(localLayout), t0.Format(dateLayout), t0.Format(dateLayout)
		}
	}
	f.ID, f.Occurrence, f.Repeat, f.Custom, f.Scope = ss.ev.id, occ.original, "custom", ruleText(ss.s), "this"
	return f, occ, editID, true, nil
}

func (a *App) getEvent(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	ss, err := a.loadEvent(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	var f web.EventForm
	ok := true
	if ss.s.rule == "" {
		f, err = a.eventForm(r.Context(), id)
	} else {
		f, _, _, ok, err = a.occurrenceForm(r.Context(), ss, r.URL.Query().Get("occurrence"))
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	} else if !ok {
		http.NotFound(w, r)
		return
	}
	a.eventPage(w, r, http.StatusOK, f, "")
}

// allEdit is an All events edit: the text fields it changes (nil if not), whether
// it changes the Members, and the edited Occurrence's One-off edit row (0 if none).
type allEdit struct {
	title, location, description any
	members                      bool
	editID                       int64
}

// storedRule is s's rule, EXDATEs and RDATEs as eventRow holds them.
func storedRule(s series) (rule any, exdates, rdates string) {
	ex, _ := json.Marshal(append([]string{}, s.exdates...))
	rd, _ := json.Marshal(append([]string{}, s.rdates...))
	return s.rule, string(ex), string(rd)
}

// saveEvent creates (id 0) or updates a single event and its Members. On a
// Series it edits an Occurrence in the scope the form picks (spec §7.4):
//   - "this" (the default) creates or updates its One-off edit row;
//   - "following" Splits the Series there, or from its first Occurrence edits it
//     all, resetting later One-off edits once the form confirms their count;
//   - "all" changes title, location, description and Members only, on the Series
//     and on each One-off edit that hadn't changed that field.
func (a *App) saveEvent(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if r.PathValue("id") != "" && id <= 0 {
		http.NotFound(w, r)
		return
	}
	var ss storedSeries
	if id != 0 {
		var err error
		if ss, err = a.loadEvent(r.Context(), id); errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		} else if err != nil {
			a.serverError(w, r, err)
			return
		}
	}
	r.ParseForm()
	// On a Series the dialog posts its rule back as custom text: unchanged, it is left as stored.
	sameRule := ss.s.rule != "" && (!r.PostForm.Has("repeat") ||
		r.PostFormValue("repeat") == "custom" && strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("rrule"), "\r\n", "\n")) == ruleText(ss.s))
	f, e, msg := a.parseEventForm(r, !sameRule)
	f.ID = id
	status := http.StatusBadRequest
	row := id                // the row to update; 0 inserts
	var editOf, original any // set for a This event edit: its Series and Occurrence
	var split, resetFrom any // set for This and following: the Occurrence a Split starts at; the first original whose One-off edits go
	var all *allEdit         // set for All events
	if ss.s.rule != "" {
		shown, occ, editID, ok, err := a.occurrenceForm(r.Context(), ss, r.URL.Query().Get("occurrence"))
		if err != nil {
			a.serverError(w, r, err)
			return
		} else if !ok {
			http.NotFound(w, r)
			return
		}
		e.calendarID, f.CalendarID, f.Occurrence, f.Scope = ss.calendarID, ss.calendarID, occ.original, cmp.Or(r.PostFormValue("scope"), "this")
		rerule := !sameRule
		if sameRule {
			e.rrule, e.exdates, e.rdates = storedRule(ss.s)
			f.Repeat, f.Custom = "custom", ruleText(ss.s)
		}
		retimed := f.AllDay != shown.AllDay || f.AllDay && (f.StartDate != shown.StartDate || f.EndDate != shown.EndDate) ||
			!f.AllDay && (f.Start != shown.Start || f.End != shown.End || f.TZ != shown.TZ)
		refuse := func(m string) {
			if msg == "" {
				msg = m
			}
		}
		switch f.Scope {
		case "this":
			if rerule {
				refuse("A repeat change applies to This and following events.")
			}
			row, editOf, original = editID, id, occ.original
			e.rrule, e.exdates, e.rdates = nil, "[]", "[]"
		case "all":
			if rerule || retimed {
				refuse("All events changes only the title, location, description and Members; change times or repeats with This event or This and following.")
			}
			// Only the fields changed in the dialog apply, so opening a One-off edit doesn't spread its own changes.
			all = &allEdit{editID: editID}
			for _, c := range []struct {
				to       *any
				got, was string
			}{{&all.title, f.Title, shown.Title}, {&all.location, f.Location, shown.Location}, {&all.description, f.Description, shown.Description}} {
				if c.got != c.was {
					*c.to = c.got
				}
			}
			ms, err := a.activeMembers(r)
			if err != nil {
				a.serverError(w, r, err)
				return
			}
			for _, m := range ms { // archived Members aren't in the picker
				all.members = all.members || f.Members[m.ID] != shown.Members[m.ID]
			}
		case "following":
			first, err := a.startsAt(r.Context(), ss, occ.original)
			var later int
			if err == nil {
				err = a.DB.QueryRowContext(r.Context(), "SELECT count(*) FROM events WHERE recurring_master_id = ? AND original_start_utc > ?", id, occ.original).Scan(&later)
			}
			if err != nil {
				a.serverError(w, r, err)
				return
			}
			if later > 0 && r.PostFormValue("reset") != strconv.Itoa(later) && msg == "" {
				f.Reset, status = later, http.StatusConflict
				refuse(fmt.Sprintf("This and following resets the %d One-off edits after this event. Save again to go ahead.", later))
			}
			resetFrom = occ.original
			if !first && msg == "" {
				row, split = 0, occ.original
				// The new Series takes only the RDATEs from the Split on, and an unchanged COUNT less the Occurrences before it.
				var rds []string
				zone := ss.zone
				if f.AllDay {
					zone = nil
				} else if zone, err = time.LoadLocation(f.TZ); err != nil {
					a.serverError(w, r, err)
					return
				}
				if rule, ok := e.rrule.(string); ok && json.Unmarshal([]byte(e.rdates), &rds) == nil {
					_, rds = splitRDates(rds, zone, occ.original)
					b, _ := json.Marshal(append([]string{}, rds...))
					e.rdates = string(b)
					if m := countRE.FindStringSubmatch(rule); m != nil && sameRule {
						to := time.Unix(occ.original, 0).UTC()
						before, err := expand(ss.ev, series{rule: ss.s.rule}, ss.zone, nil, to.AddDate(-horizonYears, 0, 0), to)
						if err != nil {
							a.serverError(w, r, err)
							return
						}
						n, _ := strconv.Atoi(m[1])
						e.rrule = countRE.ReplaceAllString(rule, "COUNT="+strconv.Itoa(max(n-len(before), 1)))
					}
				}
			}
		default:
			refuse("Pick which events to change.")
		}
	}
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
		a.eventPage(w, r, status, f, msg)
		return
	}
	tx, err := a.DB.BeginTx(r.Context(), nil)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	now := a.Now().Unix()
	typ := "updated"
	rows := []int64{row} // the rows whose Members become e.members
	switch {
	case split != nil:
		err = a.endSeriesBefore(r.Context(), tx, ss, split.(int64))
	case resetFrom != nil:
		_, err = tx.ExecContext(r.Context(), "DELETE FROM events WHERE recurring_master_id = ? AND original_start_utc >= ?", id, resetFrom)
	}
	switch {
	case err != nil:
	case all != nil:
		rows = nil // the rows whose Members follow: none unless they changed
		if all.members {
			rows = []int64{id}
			if all.editID != 0 {
				rows = append(rows, all.editID)
			}
			// One-off edits with the Series' Members follow its new ones.
			var followers *sql.Rows
			followers, err = tx.QueryContext(r.Context(), `SELECT id FROM events e WHERE recurring_master_id = ?1
				AND (SELECT group_concat(member_id, ',' ORDER BY member_id) FROM event_members WHERE event_id = e.id)
					IS (SELECT group_concat(member_id, ',' ORDER BY member_id) FROM event_members WHERE event_id = ?1)`, id)
			if err == nil {
				for followers.Next() {
					var fid int64
					if err = followers.Scan(&fid); err != nil {
						break
					}
					rows = append(rows, fid)
				}
				followers.Close()
				err = cmp.Or(err, followers.Err())
			}
		}
		if err == nil { // One-off edits keep the fields they changed
			_, err = tx.ExecContext(r.Context(), `UPDATE events SET title = CASE WHEN title = m.t THEN coalesce(?1, title) ELSE title END,
					location = CASE WHEN location = m.l THEN coalesce(?2, location) ELSE location END,
					description = CASE WHEN description = m.d THEN coalesce(?3, description) ELSE description END, updated_at = ?4
				FROM (SELECT title AS t, location AS l, description AS d FROM events WHERE id = ?5) AS m
				WHERE recurring_master_id = ?5`, all.title, all.location, all.description, now, id)
		}
		if err == nil { // the Series, and the One-off edit the dialog showed
			_, err = tx.ExecContext(r.Context(), "UPDATE events SET title = coalesce(?, title), location = coalesce(?, location), description = coalesce(?, description), updated_at = ? WHERE id IN (?, ?)",
				all.title, all.location, all.description, now, id, all.editID)
		}
	case row == 0:
		if id == 0 {
			typ = "created"
		}
		err = tx.QueryRowContext(r.Context(), `INSERT INTO events (calendar_id, title, location, description, all_day, start_utc, end_utc, start_tzid, all_day_date, all_day_end_date,
				rrule, exdates_json, rdates_json, recurring_master_id, original_start_utc, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			-- A concurrent first edit of the same Occurrence may have made its row since occurrenceAt looked.
			ON CONFLICT (recurring_master_id, original_start_utc) DO UPDATE SET title = excluded.title, location = excluded.location,
				description = excluded.description, all_day = excluded.all_day, start_utc = excluded.start_utc, end_utc = excluded.end_utc,
				start_tzid = excluded.start_tzid, all_day_date = excluded.all_day_date, all_day_end_date = excluded.all_day_end_date,
				status = 'confirmed', updated_at = excluded.updated_at
			RETURNING id`,
			e.calendarID, e.title, e.location, e.description, e.allDay, e.start, e.end, e.tzid, e.date, e.endDate,
			e.rrule, e.exdates, e.rdates, editOf, original, now).Scan(&row)
		rows[0] = row
		if err == nil && id != 0 { // archived Members aren't in the picker: keep the Series'
			_, err = tx.ExecContext(r.Context(), "INSERT OR IGNORE INTO event_members (event_id, member_id) SELECT ?, member_id FROM event_members WHERE event_id = ? AND member_id IN (SELECT id FROM members WHERE archived)", row, id)
		}
	default:
		err = tx.QueryRowContext(r.Context(), `UPDATE events SET calendar_id = ?, title = ?, location = ?, description = ?, all_day = ?,
				start_utc = ?, end_utc = ?, start_tzid = ?, all_day_date = ?, all_day_end_date = ?, rrule = ?, exdates_json = ?, rdates_json = ?, updated_at = ?
			WHERE id = ? AND `+localCalendar+" RETURNING id",
			e.calendarID, e.title, e.location, e.description, e.allDay, e.start, e.end, e.tzid, e.date, e.endDate, e.rrule, e.exdates, e.rdates, now, row).Scan(&row)
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	for _, row := range rows {
		// Archived Members are hidden from the picker, so they stay on the event.
		if _, err := tx.ExecContext(r.Context(), "DELETE FROM event_members WHERE event_id = ? AND member_id IN (SELECT id FROM members WHERE NOT archived)", row); err != nil {
			a.serverError(w, r, err)
			return
		}
		for _, m := range e.members {
			if _, err := tx.ExecContext(r.Context(), "INSERT OR IGNORE INTO event_members (event_id, member_id) SELECT ?, id FROM members WHERE id = ? AND NOT archived", row, m); err != nil {
				a.serverError(w, r, err)
				return
			}
		}
	}
	if err := tx.Commit(); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.publish(typ, "event", cmp.Or(id, row))
	http.Redirect(w, r, "/calendar?date="+e.firstDay, http.StatusSeeOther)
}

// startsAt reports whether original o is Series ss' first Occurrence: none
// comes before it that hasn't been deleted.
func (a *App) startsAt(ctx context.Context, ss storedSeries, o int64) (bool, error) {
	rows, err := a.DB.QueryContext(ctx, "SELECT original_start_utc FROM events WHERE recurring_master_id = ? AND status = 'cancelled' AND original_start_utc < ?", ss.ev.id, o)
	if err != nil {
		return false, err
	}
	var deleted []oneOff
	for rows.Next() {
		d := oneOff{cancelled: true}
		if err := rows.Scan(&d.original); err != nil {
			rows.Close()
			return false, err
		}
		deleted = append(deleted, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	to := time.Unix(o, 0).UTC()
	occs, err := expand(ss.ev, ss.s, ss.zone, deleted, to.AddDate(-horizonYears, 0, 0), to)
	return len(occs) == 0, err
}

// endSeriesBefore ends Series ss just before its Occurrence o and drops its One-off edits from o on.
func (a *App) endSeriesBefore(ctx context.Context, tx *sql.Tx, ss storedSeries, o int64) error {
	rule, _, rd := storedRule(endBefore(ss.s, ss.zone, o))
	if _, err := tx.ExecContext(ctx, "UPDATE events SET rrule = ?, rdates_json = ?, updated_at = ? WHERE id = ?", rule, rd, a.Now().Unix(), ss.ev.id); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM events WHERE recurring_master_id = ? AND original_start_utc >= ?", ss.ev.id, o)
	return err
}

// deleteEvent deletes a single event. On a Series, scope "this" (the default)
// cancels that Occurrence, "following" ends the Series before it (deleting it
// if that is its first Occurrence), and "all" deletes the Series.
func (a *App) deleteEvent(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	ss, err := a.loadEvent(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	typ := "deleted"
	if ss.s.rule == "" {
		_, err = a.DB.ExecContext(r.Context(), "DELETE FROM events WHERE id = ?", id)
	} else {
		occ, editID, ok, err2 := a.occurrenceAt(r.Context(), ss, r.URL.Query().Get("occurrence"))
		if err2 != nil {
			a.serverError(w, r, err2)
			return
		} else if !ok {
			http.NotFound(w, r)
			return
		}
		scope := cmp.Or(r.PostFormValue("scope"), "this")
		first := scope == "all"
		if scope == "following" {
			if first, err = a.startsAt(r.Context(), ss, occ.original); err != nil {
				a.serverError(w, r, err)
				return
			}
		}
		switch {
		case scope != "this" && scope != "following" && scope != "all":
			http.Error(w, "Pick which events to delete.", http.StatusBadRequest)
			return
		case first:
			_, err = a.DB.ExecContext(r.Context(), "DELETE FROM events WHERE id = ?", id)
		case scope == "following":
			typ = "updated"
			err = a.inTx(r.Context(), func(tx *sql.Tx) error { return a.endSeriesBefore(r.Context(), tx, ss, occ.original) })
		case editID != 0:
			typ = "updated"
			_, err = a.DB.ExecContext(r.Context(), "UPDATE events SET status = 'cancelled', updated_at = ? WHERE id = ?", a.Now().Unix(), editID)
		default: // a cancelled row keeps the Occurrence's times so the event CHECKs hold
			typ = "updated"
			var start, end, date, endDate any
			if occ.allDay {
				date, endDate = occ.date, occ.endDate
			} else {
				start, end = occ.start, occ.end
			}
			_, err = a.DB.ExecContext(r.Context(), `INSERT INTO events (calendar_id, title, location, description, all_day, start_utc, end_utc, start_tzid, all_day_date, all_day_end_date,
					recurring_master_id, original_start_utc, status, updated_at)
				SELECT calendar_id, title, location, description, all_day, ?, ?, start_tzid, ?, ?, id, ?, 'cancelled', ? FROM events WHERE id = ?
				ON CONFLICT (recurring_master_id, original_start_utc) DO UPDATE SET status = 'cancelled', updated_at = excluded.updated_at`,
				start, end, date, endDate, occ.original, a.Now().Unix(), id)
		}
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.publish(typ, "event", id)
	http.Redirect(w, r, "/calendar", http.StatusSeeOther)
}

// inTx runs fn in a transaction, committing if it succeeds.
func (a *App) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}
