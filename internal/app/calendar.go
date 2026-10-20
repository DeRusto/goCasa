package app

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// localCalendars is the ids of the calendars GoCasa owns; localCalendar limits
// a query on events to them. writableCalendars adds the shown Google
// calendars an event can be created on.
const (
	localCalendars    = "SELECT c.id FROM source_calendars c JOIN integrations i ON i.id = c.integration_id WHERE i.type = 'local'"
	localCalendar     = "calendar_id IN (" + localCalendars + ")"
	writableCalendars = localCalendars + " OR i.type = '" + googleType + "' AND c.enabled AND NOT c.read_only AND i.status = 'ok'"
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
func span(view string, date time.Time, weekday int) (first time.Time, n int, prev, next time.Time) {
	weekStart := func(d time.Time) time.Time { return dayAt(d, -(int(d.Weekday())-weekday+7)%7) }
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
	start, end    int64    // timed
	date, endDate string   // all-day
	colors        []string // its Members' colours, else its calendar's
	members       []int64  // its Members, in colors' order
	shown         bool     // passes the Member filter; eventsIn returns only these
	readOnly      bool     // on a feed's calendar
	synced        bool     // from Google: only its Members change here
}

func (a *App) getCalendar(w http.ResponseWriter, r *http.Request) {
	loc := a.location(r.Context())
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
	hh, err := a.household(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	first, n, prev, next := span(view, date, hh.WeekStart)
	end := dayAt(first, n)

	evs, err := a.eventsIn(r.Context(), first, end, users)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	now := a.Now().In(loc)
	p := web.CalendarPage{View: view, Date: date, Users: map[int64]bool{}, ICS: a.icsURL(), DefaultView: calendarView(view, users),
		Clock24: hh.Clock24, NowMin: now.Hour()*60 + now.Minute()}
	if p.Stale, p.BeyondGoogle, err = a.syncNotices(r.Context(), end); err != nil {
		a.serverError(w, r, err)
		return
	}
	for _, id := range users {
		p.Users[id] = true
	}
	for i := range n {
		d0 := dayAt(first, i)
		day := calendarDay(evs, d0, today, 0, clockLayout(hh.Clock24))
		day.Faded = view == "month" && d0.Month() != date.Month()
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
	if p.Calendars, err = a.calendars(r, localCalendars); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, web.Calendar(p))
}

// calendarDay is the events of evs on day d0 (local midnight); timed events
// that ended by now (unix seconds; 0: never) are Past. clock is their times' layout.
func calendarDay(evs []calendarEvent, d0, today time.Time, now int64, clock string) web.CalendarDay {
	loc := d0.Location()
	d1 := dayAt(d0, 1)
	ds := d0.Format(dateLayout)
	day := web.CalendarDay{Date: d0, Today: d0.Equal(today)}
	for _, e := range evs {
		ev := web.CalendarEvent{ID: e.id, Original: e.original, Title: e.title, Colors: e.colors, Members: e.members, ReadOnly: e.readOnly, Synced: e.synced}
		if e.allDay {
			if e.date > ds || e.endDate <= ds {
				continue
			}
			ev.Time, ev.AllDay = "All day", true
			ev.ContinuesBefore, ev.ContinuesAfter = e.date < ds, e.endDate > d1.Format(dateLayout)
		} else {
			if e.start >= d1.Unix() || e.end <= d0.Unix() {
				continue
			}
			from, to := "…", "…"
			ev.To = 24 * 60
			if e.start >= d0.Unix() {
				t := time.Unix(e.start, 0).In(loc)
				from, ev.From = t.Format(clock), t.Hour()*60+t.Minute()
			}
			if e.end <= d1.Unix() {
				t := time.Unix(e.end, 0).In(loc)
				to = t.Format(clock)
				if e.end < d1.Unix() {
					ev.To = t.Hour()*60 + t.Minute()
				}
			}
			ev.Time = from + "–" + to
			ev.Past, ev.End = e.end <= now, e.end*1000
		}
		day.Events = append(day.Events, ev)
	}
	lanes(day.Events)
	return day
}

// lanes sets each timed event's Lane and Lanes, so events that overlap sit side
// by side on the time grid; evs are by start, as eventsIn returns them.
func lanes(evs []web.CalendarEvent) {
	var group []*web.CalendarEvent // events chained by overlaps
	var ends []int                 // each lane's last end in the group
	for i := range evs {
		e := &evs[i]
		if e.AllDay {
			continue
		}
		if len(group) > 0 && e.From >= slices.Max(ends) {
			for _, g := range group {
				g.Lanes = len(ends)
			}
			group, ends = group[:0], ends[:0]
		}
		e.Lane = slices.IndexFunc(ends, func(end int) bool { return end <= e.From })
		if e.Lane < 0 {
			e.Lane, ends = len(ends), append(ends, 0)
		}
		ends[e.Lane] = max(e.To, e.From+1) // a zero-length event still takes its lane
		group = append(group, e)
	}
	for _, g := range group {
		g.Lanes = len(ends)
	}
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

// calendars is the calendars whose ids query selects.
func (a *App) calendars(r *http.Request, query string) ([]web.CalendarInfo, error) {
	rows, err := a.DB.QueryContext(r.Context(), "SELECT id, name, color FROM source_calendars WHERE id IN ("+query+") ORDER BY name, id")
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
	if _, err := a.exec(r.Context(), web.KindEvent, "INSERT INTO source_calendars (integration_id, name, color) VALUES ((SELECT id FROM integrations WHERE type = 'local'), ?, ?)", name, color); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/calendar", http.StatusSeeOther)
}

var weekdays = map[string]bool{"MO": true, "TU": true, "WE": true, "TH": true, "FR": true, "SA": true, "SU": true}

// presetRule reads the repeat, interval and byday fields of a preset (daily,
// weekly on days, monthly or yearly, every N) into a rule, marking the days in
// byDay, or returns a message for the person.
func presetRule(r *http.Request, byDay map[string]bool) (rule, msg string) {
	n := 1
	if v := r.PostFormValue("interval"); v != "" {
		var err error
		if n, err = strconv.Atoi(v); err != nil || n < 1 || n > 999 {
			return "", "Repeat every 1 to 999 days, weeks, months or years."
		}
	}
	repeat := r.PostFormValue("repeat")
	rule = "FREQ=" + strings.ToUpper(repeat) + ";INTERVAL=" + strconv.Itoa(n)
	if repeat == "weekly" && len(r.PostForm["byday"]) > 0 {
		for _, d := range r.PostForm["byday"] {
			if !weekdays[d] {
				return "", "Pick days of the week."
			}
			byDay[d] = true
		}
		rule += ";BYDAY=" + strings.Join(r.PostForm["byday"], ",")
	}
	return rule, ""
}

// anchorRule makes a monthly or yearly preset rule that starts on day keep to
// the last day of months without day's date (spec §8), instead of skipping
// them as RFC 5545 does: the 31st falls on April 30 and February 28 or 29. A
// rule with any BY part is a custom or already anchored one, and is kept.
func anchorRule(rule string, day time.Time) string {
	d := day.Day()
	switch {
	case d < 29 || strings.Contains(rule, "BY"):
		return rule
	case strings.Contains(rule, "FREQ=MONTHLY"):
		return rule + ";BYMONTHDAY=" + "28,29,30,31"[:3*(d-27)-1] + ";BYSETPOS=-1" // 28 to d
	case strings.Contains(rule, "FREQ=YEARLY") && day.Month() == time.February:
		return rule + ";BYMONTH=2;BYMONTHDAY=28,29;BYSETPOS=-1"
	}
	return rule
}

// reanchor is the rule of a Series whose Occurrence on day was moves to day
// to: a rule that is a preset plus anchorRule's own month-end anchor is
// anchored afresh from to, as a new preset Series would be; any other rule,
// or the same month and day, keeps it.
func reanchor(rule string, was, to time.Time) string {
	if was.Month() == to.Month() && was.Day() == to.Day() {
		return rule
	}
	base, _, _ := strings.Cut(rule, ";BYMONTH") // a preset's anchor is its only BY part, and last
	for _, d := range []time.Time{time.Date(2026, 1, 29, 0, 0, 0, 0, time.UTC), time.Date(2026, 1, 30, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC)} {
		if base != rule && anchorRule(base, d) == rule {
			return anchorRule(base, to)
		}
	}
	return rule
}

// startDay is c's first day, as a date in its own zone.
func startDay(c eventChange) time.Time {
	if c.allDay {
		d, _ := time.Parse(dateLayout, c.date.(string))
		return d
	}
	zone, _ := time.LoadLocation(c.tzid.(string)) // parseEventForm or the store checked it
	return time.Unix(c.start.(int64), 0).In(zone)
}

// parseRepeat reads the dialog's repeat fields into a rule and its EXDATEs and
// RDATEs (as stored), or a message for the person.
func parseRepeat(f *web.EventForm, r *http.Request) (rule string, exdates, rdates []string, msg string) {
	f.Repeat, f.Interval, f.Custom = r.PostFormValue("repeat"), r.PostFormValue("interval"), strings.TrimSpace(r.PostFormValue("rrule"))
	f.ByDay = map[string]bool{}
	switch f.Repeat {
	case "":
		return "", nil, nil, ""
	case "daily", "weekly", "monthly", "yearly":
		if rule, msg = presetRule(r, f.ByDay); msg != "" {
			return "", nil, nil, msg
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
func (a *App) parseEventForm(r *http.Request, withRepeat bool) (web.EventForm, eventChange, string) {
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
	e := eventChange{calendarID: f.CalendarID, title: f.Title, location: f.Location, description: f.Description, allDay: f.AllDay, members: parseIDs(r.PostForm["members"]), exdates: "[]", rdates: "[]"}
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
		if rule != "" && f.Repeat != "custom" {
			e.rrule = anchorRule(rule, d0)
		}
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
	if rule != "" && f.Repeat != "custom" {
		e.rrule = anchorRule(rule, t0)
	}
	e.firstDay = t0.In(a.location(r.Context())).Format(dateLayout)
	return f, e, ""
}

func (a *App) eventPage(w http.ResponseWriter, r *http.Request, status int, f web.EventForm, errMsg string) {
	query := localCalendars // a local event stays on a local calendar
	if f.ID == 0 {
		query = writableCalendars
	}
	cals, err := a.calendars(r, query)
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
	loc := a.location(r.Context())
	date, err := time.ParseInLocation(dateLayout, r.URL.Query().Get("date"), loc)
	if err != nil {
		date = dayAt(a.Now().In(loc), 0)
	}
	d := date.Format(dateLayout)
	a.eventPage(w, r, http.StatusOK, web.EventForm{
		Start: d + "T09:00", End: d + "T10:00", TZ: loc.String(), StartDate: d, EndDate: d, Members: map[int64]bool{}, ByDay: map[string]bool{},
	}, "")
}

// eventForm is c as the dialog shows it.
func (a *App) eventForm(ctx context.Context, c eventChange) (web.EventForm, error) {
	f := web.EventForm{CalendarID: c.calendarID, Title: c.title, Location: c.location, Description: c.description, AllDay: c.allDay,
		Members: map[int64]bool{}, ByDay: map[string]bool{}}
	for _, m := range c.members {
		f.Members[m] = true
	}
	if c.allDay {
		last, _ := time.Parse(dateLayout, c.endDate.(string))
		f.StartDate, f.EndDate = c.date.(string), last.AddDate(0, 0, -1).Format(dateLayout)
		f.TZ = a.location(ctx).String()
		f.Start, f.End = f.StartDate+"T09:00", f.StartDate+"T10:00"
		return f, nil
	}
	zone, err := time.LoadLocation(c.tzid.(string))
	if err != nil {
		return f, err
	}
	f.TZ = c.tzid.(string)
	t0, t1 := time.Unix(c.start.(int64), 0).In(zone), time.Unix(c.end.(int64), 0).In(zone)
	f.Start, f.End = t0.Format(localLayout), t1.Format(localLayout)
	f.StartDate, f.EndDate = t0.Format(dateLayout), t0.Format(dateLayout)
	return f, nil
}

func (a *App) getEvent(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	fo, err := a.findOccurrence(r.Context(), id, r.URL.Query().Get("occurrence"))
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	c, err := a.occurrenceNow(r.Context(), fo)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	f, err := a.eventForm(r.Context(), c)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	f.ID = id
	if fo.ss.s.rule != "" {
		f.Occurrence, f.Repeat, f.Custom, f.Scope = fo.occ.original, "custom", ruleText(fo.ss.s), string(scopeThis)
	}
	a.eventPage(w, r, http.StatusOK, f, "")
}

// saveEvent creates (id 0) or updates an event from the dialog; on a Series,
// in the Edit scope the form picks (see writeEvent).
func (a *App) saveEvent(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if r.PathValue("id") != "" && id <= 0 {
		http.NotFound(w, r)
		return
	}
	occurrence := r.URL.Query().Get("occurrence")
	var fo found
	if id != 0 {
		var err error
		if fo, err = a.findOccurrence(r.Context(), id, occurrence); errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		} else if err != nil {
			a.serverError(w, r, err)
			return
		}
	}
	s := fo.ss.s
	r.ParseForm()
	// On a Series the dialog posts its rule back as custom text: unchanged, it is left as stored.
	sameRule := s.rule != "" && (!r.PostForm.Has("repeat") ||
		r.PostFormValue("repeat") == "custom" && strings.TrimSpace(strings.ReplaceAll(r.PostFormValue("rrule"), "\r\n", "\n")) == ruleText(s))
	f, c, msg := a.parseEventForm(r, !sameRule)
	c.rerule = !sameRule
	c.confirmReset, _ = strconv.Atoi(r.PostFormValue("reset"))
	sc := scope(cmp.Or(r.PostFormValue("scope"), string(scopeThis)))
	f.ID = id
	if s.rule != "" {
		f.CalendarID, f.Occurrence, f.Scope = fo.ss.calendarID, fo.occ.original, string(sc)
		if sameRule {
			f.Repeat, f.Custom = "custom", ruleText(s)
		}
	}
	if msg != "" {
		a.eventPage(w, r, http.StatusBadRequest, f, msg)
		return
	}
	if id == 0 && a.createGoogle(w, r, f, c) {
		return
	}
	_, err := a.writeEvent(r.Context(), id, occurrence, sc, c)
	var no *refusal
	switch {
	case errors.As(err, &no):
		f.Reset = no.reset
		a.eventPage(w, r, no.code(), f, no.msg)
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	case err != nil:
		a.serverError(w, r, err)
	default:
		http.Redirect(w, r, "/calendar?date="+c.firstDay, http.StatusSeeOther)
	}
}

// deleteEvent deletes an event; on a Series, in the Edit scope the form picks (see removeEvent).
func (a *App) deleteEvent(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	err := a.removeEvent(r.Context(), id, r.URL.Query().Get("occurrence"), scope(cmp.Or(r.PostFormValue("scope"), string(scopeThis))))
	var no *refusal
	switch {
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	case errors.As(err, &no):
		http.Error(w, no.msg, no.code())
	case err != nil:
		a.serverError(w, r, err)
	default:
		http.Redirect(w, r, "/calendar", http.StatusSeeOther)
	}
}
