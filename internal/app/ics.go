package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
)

const prodID = "-//GoCasa//GoCasa//EN"

// icsURL is the absolute address of the ICS export; ?calendars=1,2 picks calendars.
func (a *App) icsURL() string { return a.Config.BaseURL.JoinPath("calendar.ics").String() }

// getICS exports the calendars ?calendars= names (all if none) as iCalendar:
// each Series once with its RRULE, EXDATEs and RDATEs, and each One-off edit
// as a RECURRENCE-ID override. Viewing is never locked, so neither is this.
// shortcut: TZIDs are IANA names with no VTIMEZONE (Google, Apple and Nextcloud accept them); add VTIMEZONEs if a strict client needs them.
func (a *App) getICS(w http.ResponseWriter, r *http.Request) {
	filter := ""
	if ids := parseIDs(r.URL.Query()["calendars"]); len(ids) > 0 {
		b, _ := json.Marshal(ids)
		filter = string(b)
	}
	rows, err := a.DB.QueryContext(r.Context(), `SELECT id, title, description, location, all_day, coalesce(start_utc, 0), coalesce(end_utc, 0),
			coalesce(start_tzid, ''), coalesce(all_day_date, ''), coalesce(all_day_end_date, ''), coalesce(rrule, ''), exdates_json, rdates_json,
			coalesce(recurring_master_id, 0), coalesce(original_start_utc, 0), status = 'cancelled', updated_at
		FROM events WHERE ?1 = '' OR calendar_id IN (SELECT value FROM json_each(?1))
		ORDER BY recurring_master_id IS NOT NULL, id`, filter) // masters before their edits
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer rows.Close()

	cal := ical.NewCalendar()
	cal.Props.SetText(ical.PropVersion, "2.0")
	cal.Props.SetText(ical.PropProductID, prodID)
	type master struct {
		ev   *ical.Event
		zone *time.Location // nil when all-day
	}
	masters := map[int64]master{}
	for rows.Next() {
		var id, start, end, masterID, original, updated int64
		var title, description, location, tzid, date, endDate, rule, ex, rd string
		var allDay, cancelled bool
		if err := rows.Scan(&id, &title, &description, &location, &allDay, &start, &end, &tzid, &date, &endDate, &rule, &ex, &rd,
			&masterID, &original, &cancelled, &updated); err != nil {
			a.serverError(w, r, err)
			return
		}
		m, isEdit := masters[masterID]
		if masterID != 0 && !isEdit || masterID == 0 && cancelled {
			continue // shortcut: cancelled single rows come only from sync (spec §7.2), which this skips
		}
		if cancelled {
			m.ev.Props.Add(originalProp(ical.PropExceptionDates, original, m.zone))
			continue
		}
		var zone *time.Location
		if !allDay {
			if zone, err = time.LoadLocation(tzid); err != nil {
				a.serverError(w, r, err)
				return
			}
		}

		e := ical.NewEvent()
		uid := id
		if isEdit {
			uid = masterID
			e.Props.Set(originalProp(ical.PropRecurrenceID, original, m.zone))
		}
		e.Props.SetText(ical.PropUID, fmt.Sprintf("event-%d@%s", uid, a.Config.BaseURL.Host))
		e.Props.SetDateTime(ical.PropDateTimeStamp, time.Unix(updated, 0).UTC())
		e.Props.SetText(ical.PropSummary, title)
		if description != "" {
			e.Props.SetText(ical.PropDescription, description)
		}
		if location != "" {
			e.Props.SetText(ical.PropLocation, location)
		}
		if allDay {
			d0, err0 := time.Parse(dateLayout, date)
			d1, err1 := time.Parse(dateLayout, endDate)
			if err := errors.Join(err0, err1); err != nil {
				a.serverError(w, r, err)
				return
			}
			e.Props.SetDate(ical.PropDateTimeStart, d0)
			e.Props.SetDate(ical.PropDateTimeEnd, d1)
		} else {
			e.Props.SetDateTime(ical.PropDateTimeStart, time.Unix(start, 0).In(zone))
			e.Props.SetDateTime(ical.PropDateTimeEnd, time.Unix(end, 0).In(zone))
		}
		if rule != "" {
			ev := calendarEvent{allDay: allDay, start: start, end: end, date: date, endDate: endDate}
			rule, first, offRule, err := icsSeries(ev, rule, zone)
			if err != nil {
				a.serverError(w, r, err)
				return
			}
			p := ical.NewProp(ical.PropRecurrenceRule)
			p.Value = icsRule(rule, zone)
			e.Props.Set(p)
			if offRule {
				e.Props.Add(originalProp(ical.PropExceptionDates, first, zone))
			}
			for _, l := range []struct {
				name, json string
			}{{ical.PropExceptionDates, ex}, {ical.PropRecurrenceDates, rd}} {
				var ds []string
				if err := json.Unmarshal([]byte(l.json), &ds); err != nil {
					a.serverError(w, r, err)
					return
				}
				for _, d := range ds {
					if p, ok := storedDateProp(l.name, d, zone); ok {
						e.Props.Add(p)
					}
				}
			}
			masters[id] = master{e, zone}
		}
		cal.Children = append(cal.Children, e.Component)
	}
	if err := rows.Err(); err != nil {
		a.serverError(w, r, err)
		return
	}

	w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
	if len(cal.Children) == 0 { // go-ical refuses to encode an empty calendar; a subscriber still needs a valid feed
		io.WriteString(w, "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:"+prodID+"\r\nEND:VCALENDAR\r\n")
		return
	}
	var buf bytes.Buffer
	if err := ical.NewEncoder(&buf).Encode(cal); err != nil {
		a.serverError(w, r, err)
		return
	}
	w.Write(buf.Bytes())
}

// originalProp is an Occurrence's original (see expand) as a date-time in its
// Series' zone, or a date when the Series is all-day (zone nil).
func originalProp(name string, original int64, zone *time.Location) *ical.Prop {
	p := ical.NewProp(name)
	if zone == nil {
		p.SetDate(time.Unix(original, 0).UTC())
	} else {
		p.SetDateTime(time.Unix(original, 0).In(zone))
	}
	return p
}

// storedDateProp is a stored EXDATE or RDATE (a wall time in zone, or a date
// when zone is nil); false for a wall time in a spring-forward gap, which
// expand never makes an Occurrence.
func storedDateProp(name, v string, zone *time.Location) (*ical.Prop, bool) {
	p := ical.NewProp(name)
	if zone == nil {
		d, err := time.Parse(dateLayout, v)
		p.SetDate(d)
		return p, err == nil
	}
	t, err := parseLocal(v, zone)
	p.SetDateTime(t)
	return p, err == nil && t.Format(localLayout) == v
}

// icsRule is a stored rule with UNTIL in the form RFC 5545 asks for alongside
// DTSTART: a date when all-day (zone nil), else UTC. A date or wall-time UNTIL
// on a timed Series is read in its zone, as expand reads it.
func icsRule(rule string, zone *time.Location) string {
	parts := strings.Split(rule, ";")
	for i, p := range parts {
		k, v, _ := strings.Cut(p, "=")
		if k != "UNTIL" || len(v) < 8 {
			continue
		}
		if zone == nil {
			parts[i] = "UNTIL=" + v[:8]
			continue
		}
		if len(v) == 8 {
			v += "T000000"
		}
		if t, ok := untilInstant(v, zone); ok {
			parts[i] = "UNTIL=" + t.UTC().Format("20060102T150405Z")
		}
	}
	return strings.Join(parts, ";")
}

// icsSeries is a stored rule as a subscriber must get it to make the
// Occurrences expand does. RFC 5545 counts DTSTART as an Occurrence even when
// the rule doesn't make it (a Sunday start on Mondays), and some clients
// don't: offRule then asks for an EXDATE at first, DTSTART's original. COUNT
// would then mean different things to them, so it becomes an UNTIL at the last
// Occurrence; as it also does beside an UNTIL, which RFC 5545 forbids and a
// Split stores.
func icsSeries(ev calendarEvent, rule string, zone *time.Location) (out string, first int64, offRule bool, err error) {
	first = ev.start
	if ev.allDay {
		d, _ := time.Parse(dateLayout, ev.date)
		first = d.Unix()
	}
	from := time.Unix(first, 0).UTC()
	to := from.AddDate(horizonYears, 0, 0)
	occs, err := occurrences(ev, series{rule: rule}, zone, nil, from, to, 1)
	if err != nil {
		return rule, first, false, err
	}
	offRule = len(occs) == 0 || occs[0].original != first
	m := countRE.FindStringSubmatch(rule)
	if m == nil || !offRule && !strings.Contains(rule, "UNTIL=") {
		return rule, first, offRule, nil
	}
	n, _ := strconv.Atoi(m[1])
	if occs, err = occurrences(ev, series{rule: rule}, zone, nil, from, to, n); err != nil {
		return rule, first, false, err
	}
	last := ev // no Occurrences: an UNTIL at DTSTART, which the EXDATE removes
	if len(occs) > 0 {
		last = occs[len(occs)-1]
	}
	until := time.Unix(last.start, 0).UTC().Format("20060102T150405Z")
	if ev.allDay {
		until = strings.ReplaceAll(last.date, "-", "")
	}
	var parts []string
	for _, p := range strings.Split(rule, ";") {
		if k, _, _ := strings.Cut(p, "="); k != "COUNT" && k != "UNTIL" {
			parts = append(parts, p)
		}
	}
	return strings.Join(append(parts, "UNTIL="+until), ";"), first, offRule, nil
}
