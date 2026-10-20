package app

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// scope is an Edit scope: which Occurrences of a Series a change or deletion applies to.
type scope string

const (
	scopeThis      scope = "this"
	scopeFollowing scope = "following"
	scopeAll       scope = "all"
)

// eventChange is a local event as a change sets it, in its stored form.
type eventChange struct {
	calendarID                      int64
	title, location, description    string
	allDay                          bool
	start, end, tzid, date, endDate any // NULL for the other kind of event
	rrule                           any // NULL unless it repeats
	exdates, rdates                 string
	members                         []int64
	firstDay                        string // in the Household's zone, to show after saving
	rerule                          bool   // on a Series: sets rrule, exdates and rdates; false keeps its own
	confirmReset                    int    // the One-off edits a This and following change may reset
}

// refusal is a change the module won't make, with a message for the person.
// conflict marks one that may succeed when redone; reset is the One-off edits
// a This and following change would reset, to confirm; status, if set, is
// the answer's (e.g. when Google refused it).
type refusal struct {
	msg      string
	conflict bool
	reset    int
	status   int
}

func (r *refusal) Error() string { return r.msg }

// code is the HTTP status that answers r.
func (r *refusal) code() int {
	switch {
	case r.status != 0:
		return r.status
	case r.conflict:
		return http.StatusConflict
	}
	return http.StatusBadRequest
}

var errSeriesChanged = &refusal{msg: "This Series changed while you were editing it. Open it again and redo your change.", conflict: true}

// storedSeries is a local event that isn't a One-off edit; s.rule is "" for a single event.
type storedSeries struct {
	ev         calendarEvent
	calendarID int64
	zone       *time.Location // nil when all-day
	s          series
	oneOffs    []oneOff
}

// found is a local event and, on a Series, one of its Occurrences.
type found struct {
	ss     storedSeries
	occ    calendarEvent // zero for a single event
	editID int64         // the Occurrence's One-off edit row, 0 if none
}

// findOccurrence finds local event id and, on a Series, its Occurrence whose
// key is occurrence ("" is the first). It is sql.ErrNoRows if either is
// missing or deleted, or id is a One-off edit row.
func (a *App) findOccurrence(ctx context.Context, id int64, occurrence string) (found, error) {
	ss, err := a.loadEvent(ctx, id)
	if err != nil || ss.s.rule == "" {
		return found{ss: ss}, err
	}
	occ, editID, ok, err := a.occurrenceAt(ctx, ss, occurrence)
	if err == nil && !ok {
		err = sql.ErrNoRows
	}
	return found{ss: ss, occ: occ, editID: editID}, err
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

// occurrenceNow is fo's event as it stands, as a change would set it: its
// One-off edit, else the Series' own fields at the Occurrence's time. Its rule
// is left unset.
func (a *App) occurrenceNow(ctx context.Context, fo found) (eventChange, error) {
	id := cmp.Or(fo.editID, fo.ss.ev.id)
	c := eventChange{exdates: "[]", rdates: "[]"}
	var start, end sql.NullInt64
	var tzid, date, endDate sql.NullString
	err := a.DB.QueryRowContext(ctx, "SELECT calendar_id, title, location, description, all_day, start_utc, end_utc, start_tzid, all_day_date, all_day_end_date FROM events WHERE id = ?", id).
		Scan(&c.calendarID, &c.title, &c.location, &c.description, &c.allDay, &start, &end, &tzid, &date, &endDate)
	if err != nil {
		return c, err
	}
	if fo.ss.s.rule != "" && fo.editID == 0 {
		start.Int64, end.Int64, date.String, endDate.String = fo.occ.start, fo.occ.end, fo.occ.date, fo.occ.endDate
	}
	if c.allDay {
		c.date, c.endDate = date.String, endDate.String
	} else {
		c.start, c.end, c.tzid = start.Int64, end.Int64, tzid.String
	}
	rows, err := a.DB.QueryContext(ctx, "SELECT member_id FROM event_members WHERE event_id = ? ORDER BY member_id", id)
	if err != nil {
		return c, err
	}
	defer rows.Close()
	for rows.Next() {
		var m int64
		if err := rows.Scan(&m); err != nil {
			return c, err
		}
		c.members = append(c.members, m)
	}
	return c, rows.Err()
}

// eventsIn is the local events and Occurrences touching [from, to) that pass
// the Member filter users (none: all), all-day first, then by start.
func (a *App) eventsIn(ctx context.Context, from, to time.Time, users []int64) ([]calendarEvent, error) {
	filter := ""
	if len(users) > 0 {
		b, _ := json.Marshal(users)
		filter = string(b)
	}
	// Single events in the window, plus every Series that starts before its end or has a One-off edit in it, with all its One-off edits.
	rows, err := a.DB.QueryContext(ctx, `SELECT e.id, e.title, e.all_day, coalesce(e.start_utc, 0), coalesce(e.end_utc, 0),
			coalesce(e.all_day_date, ''), coalesce(e.all_day_end_date, ''), c.color,
			coalesce((SELECT group_concat(m.color, ',' ORDER BY m.name, m.id) FROM event_members em JOIN members m ON m.id = em.member_id WHERE em.event_id = e.id), ''),
			?5 = '' OR NOT EXISTS (SELECT 1 FROM event_members WHERE event_id = e.id)
				OR EXISTS (SELECT 1 FROM event_members WHERE event_id = e.id AND member_id IN (SELECT value FROM json_each(?5))),
			coalesce(e.start_tzid, ''), coalesce(e.rrule, ''), e.exdates_json, e.rdates_json,
			coalesce(e.recurring_master_id, 0), coalesce(e.original_start_utc, 0), e.status = 'cancelled', c.read_only, i.type = '`+googleType+`'
		FROM events e JOIN source_calendars c ON c.id = e.calendar_id JOIN integrations i ON i.id = c.integration_id
		-- A cancelled single event or Series is one gone from its feed; a cancelled One-off edit is a deleted Occurrence.
		WHERE c.enabled AND (e.recurring_master_id IS NOT NULL OR e.status = 'confirmed') AND (e.rrule IS NULL AND e.recurring_master_id IS NULL
				AND (NOT e.all_day AND e.start_utc < ?2 AND e.end_utc > ?1 OR e.all_day AND e.all_day_date < ?4 AND e.all_day_end_date > ?3)
			OR coalesce(e.recurring_master_id, e.id) IN (SELECT id FROM events WHERE rrule IS NOT NULL AND coalesce(start_utc < ?2, all_day_date < ?4))
			OR coalesce(e.recurring_master_id, e.id) IN (SELECT recurring_master_id FROM events WHERE recurring_master_id IS NOT NULL
				AND (NOT all_day AND start_utc < ?2 AND end_utc > ?1 OR all_day AND all_day_date < ?4 AND all_day_end_date > ?3)))
		ORDER BY e.recurring_master_id IS NOT NULL`, // masters before their edits
		from.Unix(), to.Unix(), from.Format(dateLayout), to.Format(dateLayout), filter)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var evs []calendarEvent
	var masters []*storedSeries
	byID := map[int64]*storedSeries{}
	for rows.Next() {
		var e calendarEvent
		var tzid, ex, rd, calendarColor, memberColors string
		var s series
		var o oneOff
		var masterID int64
		if err := rows.Scan(&e.id, &e.title, &e.allDay, &e.start, &e.end, &e.date, &e.endDate, &calendarColor, &memberColors, &e.shown,
			&tzid, &s.rule, &ex, &rd, &masterID, &o.original, &o.cancelled, &e.readOnly, &e.synced); err != nil {
			return nil, err
		}
		e.colors = []string{calendarColor}
		if memberColors != "" {
			e.colors = strings.Split(memberColors, ",")
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
				return nil, err
			}
			masters = append(masters, ss)
			byID[e.id] = ss
		default:
			evs = append(evs, e)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, m := range masters {
		occs, err := expand(m.ev, m.s, m.zone, m.oneOffs, from, to)
		if err != nil {
			return nil, err
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
	return evs, nil
}

// allEdit is an All events edit: the text fields it changes (nil if not), whether
// it changes the Members, and the edited Occurrence's One-off edit row (0 if none).
type allEdit struct {
	title, location, description any
	members                      bool
	editID                       int64
}

// writeEvent creates (id 0) or updates a local single event and its Members,
// and publishes the change. On a Series it edits the Occurrence whose key is
// occurrence in Edit scope sc (spec §7.4):
//   - This event creates or updates its One-off edit row;
//   - This and following Splits the Series there, or from its first Occurrence
//     edits it all, resetting later One-off edits once c confirms their count;
//   - All events changes title, location, description and Members only, on the
//     Series and on each One-off edit that hadn't changed that field.
//
// It returns the event written: the new Series on a Split, else the Series or
// single event. A change it won't make is a *refusal; a missing event or
// Occurrence is sql.ErrNoRows.
func (a *App) writeEvent(ctx context.Context, id int64, occurrence string, sc scope, c eventChange) (int64, error) {
	var fo found
	if id != 0 {
		var err error
		if fo, err = a.findOccurrence(ctx, id, occurrence); err != nil {
			return 0, err
		}
	}
	ss, occ := fo.ss, fo.occ
	var zone *time.Location // the change's own, nil when all-day
	if !c.allDay {
		var err error
		if zone, err = time.LoadLocation(c.tzid.(string)); err != nil {
			return 0, err
		}
	}
	row := id                  // the row to update; 0 inserts
	var editOf, original any   // set for a This event edit: its Series and Occurrence
	var splitAt, resetFrom any // set for This and following: the Occurrence a Split starts at; the first original whose One-off edits go
	var all *allEdit           // set for All events
	if ss.s.rule != "" {
		was, err := a.occurrenceNow(ctx, fo)
		if err != nil {
			return 0, err
		}
		c.calendarID = ss.calendarID
		if !c.rerule {
			s := ss.s
			if c.allDay != ss.ev.allDay { // stored dates are in the old kind's form
				clock := "" // HH:MM, used only when becoming timed
				if !c.allDay {
					clock = time.Unix(c.start.(int64), 0).In(zone).Format("15:04")
				}
				s.exdates, s.rdates = retype(s.exdates, c.allDay, clock), retype(s.rdates, c.allDay, clock)
			}
			c.rrule, c.exdates, c.rdates = storedRule(s)
		}
		retimed := c.allDay != was.allDay || shownTimes(c) != shownTimes(was)
		switch sc {
		case scopeThis:
			if c.rerule {
				return 0, &refusal{msg: "A repeat change applies to This and following events."}
			}
			row, editOf, original = fo.editID, id, occ.original
			c.rrule, c.exdates, c.rdates = nil, "[]", "[]"
		case scopeAll:
			if c.rerule || retimed {
				return 0, &refusal{msg: "All events changes only the title, location, description and Members; change times or repeats with This event or This and following."}
			}
			// Only the fields changed from the Occurrence apply, so editing from a One-off edit doesn't spread its own changes.
			all = &allEdit{editID: fo.editID}
			for _, f := range []struct {
				to       *any
				got, was string
			}{{&all.title, c.title, was.title}, {&all.location, c.location, was.location}, {&all.description, c.description, was.description}} {
				if f.got != f.was {
					*f.to = f.got
				}
			}
			active, err := a.activeMemberIDs(ctx)
			if err != nil {
				return 0, err
			}
			for _, m := range active { // archived Members can't be picked
				all.members = all.members || slices.Contains(c.members, m) != slices.Contains(was.members, m)
			}
		case scopeFollowing:
			first, err := a.startsAt(ctx, ss, occ.original)
			var later int
			if err == nil {
				err = a.DB.QueryRowContext(ctx, "SELECT count(*) FROM events WHERE recurring_master_id = ? AND original_start_utc > ?", id, occ.original).Scan(&later)
			}
			if err != nil {
				return 0, err
			}
			if later > 0 && c.confirmReset != later {
				return 0, &refusal{msg: fmt.Sprintf("This and following resets the %d One-off edits after this event. Save again to go ahead.", later), conflict: true, reset: later}
			}
			resetFrom = occ.original
			if !first {
				row, splitAt = 0, occ.original
				// The new Series takes only the RDATEs from the Split on, and an unchanged COUNT less the Occurrences before it.
				// c's RDATEs are split in c's own zone and kind, which a change may have moved from the Series'.
				var rds []string
				if _, ok := c.rrule.(string); ok && json.Unmarshal([]byte(c.rdates), &rds) == nil {
					_, rds = splitRDates(rds, zone, occ.original)
					b, _ := json.Marshal(append([]string{}, rds...))
					c.rdates = string(b)
					if !c.rerule {
						_, after, err := split(ss.ev, series{rule: ss.s.rule}, ss.zone, occ.original)
						if err != nil {
							return 0, err
						}
						c.rrule = after.rule
					}
				}
			}
		default:
			return 0, &refusal{msg: "Pick which events to change."}
		}
	}
	var ok bool
	if err := a.DB.QueryRowContext(ctx, "SELECT ? IN ("+localCalendars+")", c.calendarID).Scan(&ok); err != nil {
		return 0, err
	}
	if !ok {
		return 0, &refusal{msg: "Pick a calendar."}
	}

	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := a.Now().Unix()
	rows := []int64{row} // the rows whose Members become c.members
	switch {
	case splitAt != nil:
		err = a.endSeriesBefore(ctx, tx, ss, splitAt.(int64))
	case resetFrom != nil:
		_, err = tx.ExecContext(ctx, "DELETE FROM events WHERE recurring_master_id = ? AND original_start_utc >= ?", id, resetFrom)
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
			followers, err = tx.QueryContext(ctx, `SELECT id FROM events e WHERE recurring_master_id = ?1
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
			_, err = tx.ExecContext(ctx, `UPDATE events SET title = CASE WHEN title = m.t THEN coalesce(?1, title) ELSE title END,
					location = CASE WHEN location = m.l THEN coalesce(?2, location) ELSE location END,
					description = CASE WHEN description = m.d THEN coalesce(?3, description) ELSE description END, updated_at = ?4
				FROM (SELECT title AS t, location AS l, description AS d FROM events WHERE id = ?5) AS m
				WHERE recurring_master_id = ?5`, all.title, all.location, all.description, now, id)
		}
		if err == nil { // the Series, and the One-off edit the dialog showed
			_, err = tx.ExecContext(ctx, "UPDATE events SET title = coalesce(?, title), location = coalesce(?, location), description = coalesce(?, description), updated_at = ? WHERE id IN (?, ?)",
				all.title, all.location, all.description, now, id, all.editID)
		}
	case row == 0:
		err = tx.QueryRowContext(ctx, `INSERT INTO events (calendar_id, title, location, description, all_day, start_utc, end_utc, start_tzid, all_day_date, all_day_end_date,
				rrule, exdates_json, rdates_json, recurring_master_id, original_start_utc, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			-- A concurrent first edit of the same Occurrence may have made its row since occurrenceAt looked.
			ON CONFLICT (recurring_master_id, original_start_utc) DO UPDATE SET title = excluded.title, location = excluded.location,
				description = excluded.description, all_day = excluded.all_day, start_utc = excluded.start_utc, end_utc = excluded.end_utc,
				start_tzid = excluded.start_tzid, all_day_date = excluded.all_day_date, all_day_end_date = excluded.all_day_end_date,
				status = 'confirmed', updated_at = excluded.updated_at
			RETURNING id`,
			c.calendarID, c.title, c.location, c.description, c.allDay, c.start, c.end, c.tzid, c.date, c.endDate,
			c.rrule, c.exdates, c.rdates, editOf, original, now).Scan(&row)
		rows[0] = row
		if err == nil && id != 0 { // archived Members can't be picked: keep the Series'
			_, err = tx.ExecContext(ctx, "INSERT OR IGNORE INTO event_members (event_id, member_id) SELECT ?, member_id FROM event_members WHERE event_id = ? AND member_id IN (SELECT id FROM members WHERE archived)", row, id)
		}
	default:
		err = tx.QueryRowContext(ctx, `UPDATE events SET calendar_id = ?, title = ?, location = ?, description = ?, all_day = ?,
				start_utc = ?, end_utc = ?, start_tzid = ?, all_day_date = ?, all_day_end_date = ?, rrule = ?, exdates_json = ?, rdates_json = ?, updated_at = ?
			WHERE id = ? AND `+localCalendar+" RETURNING id",
			c.calendarID, c.title, c.location, c.description, c.allDay, c.start, c.end, c.tzid, c.date, c.endDate, c.rrule, c.exdates, c.rdates, now, row).Scan(&row)
	}
	if err != nil {
		return 0, err
	}
	for _, row := range rows {
		// Archived Members can't be picked, so they stay on the event.
		if _, err := tx.ExecContext(ctx, "DELETE FROM event_members WHERE event_id = ? AND member_id IN (SELECT id FROM members WHERE NOT archived)", row); err != nil {
			return 0, err
		}
		for _, m := range c.members {
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO event_members (event_id, member_id) SELECT ?, id FROM members WHERE id = ? AND NOT archived", row, m); err != nil {
				return 0, err
			}
		}
	}
	if err := a.commit(tx, KindEvent); err != nil {
		return 0, err
	}
	if splitAt != nil {
		return row, nil
	}
	return cmp.Or(id, row), nil
}

// removeEvent deletes local single event id and publishes it. On a Series, scope
// This event cancels the Occurrence whose key is occurrence, This and following
// ends the Series before it (deleting it if that is its first Occurrence), and
// All events deletes the Series. Errors are as writeEvent's.
func (a *App) removeEvent(ctx context.Context, id int64, occurrence string, sc scope) error {
	fo, err := a.findOccurrence(ctx, id, occurrence)
	if err != nil {
		return err
	}
	ss, occ := fo.ss, fo.occ
	if ss.s.rule == "" {
		_, err = a.exec(ctx, KindEvent, "DELETE FROM events WHERE id = ?", id)
	} else {
		first := sc == scopeAll
		if sc == scopeFollowing {
			if first, err = a.startsAt(ctx, ss, occ.original); err != nil {
				return err
			}
		}
		switch {
		case sc != scopeThis && sc != scopeFollowing && sc != scopeAll:
			return &refusal{msg: "Pick which events to delete."}
		case first:
			_, err = a.exec(ctx, KindEvent, "DELETE FROM events WHERE id = ?", id)
		case sc == scopeFollowing:
			err = a.inTx(ctx, func(tx *sql.Tx) error { return a.endSeriesBefore(ctx, tx, ss, occ.original) }, KindEvent)
		case fo.editID != 0:
			_, err = a.exec(ctx, KindEvent, "UPDATE events SET status = 'cancelled', updated_at = ? WHERE id = ?", a.Now().Unix(), fo.editID)
		default: // a cancelled row keeps the Occurrence's times so the event CHECKs hold
			var start, end, date, endDate any
			if occ.allDay {
				date, endDate = occ.date, occ.endDate
			} else {
				start, end = occ.start, occ.end
			}
			_, err = a.exec(ctx, KindEvent, `INSERT INTO events (calendar_id, title, location, description, all_day, start_utc, end_utc, start_tzid, all_day_date, all_day_end_date,
					recurring_master_id, original_start_utc, status, updated_at)
				SELECT calendar_id, title, location, description, all_day, ?, ?, start_tzid, ?, ?, id, ?, 'cancelled', ? FROM events WHERE id = ?
				ON CONFLICT (recurring_master_id, original_start_utc) DO UPDATE SET status = 'cancelled', updated_at = excluded.updated_at`,
				start, end, date, endDate, occ.original, a.Now().Unix(), id)
		}
	}
	return err
}

// activeMemberIDs is the ids of the Members who aren't archived.
func (a *App) activeMemberIDs(ctx context.Context) ([]int64, error) {
	rows, err := a.DB.QueryContext(ctx, "SELECT id FROM members WHERE NOT archived")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// shownTimes is c's dates, or its zone and wall times, as the dialog shows
// them. Unlike the instants, they don't change when an end in a fall-back
// hour reads back as its first offset.
func shownTimes(c eventChange) string {
	if c.allDay {
		return c.date.(string) + " " + c.endDate.(string)
	}
	zone, err := time.LoadLocation(c.tzid.(string))
	if err != nil {
		return fmt.Sprint(c.tzid, c.start, c.end)
	}
	return c.tzid.(string) + " " + time.Unix(c.start.(int64), 0).In(zone).Format(localLayout) + " " + time.Unix(c.end.(int64), 0).In(zone).Format(localLayout)
}

// storedRule is s's rule, EXDATEs and RDATEs as eventChange holds them.
func storedRule(s series) (rule any, exdates, rdates string) {
	ex, _ := json.Marshal(append([]string{}, s.exdates...))
	rd, _ := json.Marshal(append([]string{}, s.rdates...))
	return s.rule, string(ex), string(rd)
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
	before, _, err := split(ss.ev, ss.s, ss.zone, o)
	if err != nil {
		return err
	}
	rule, _, rd := storedRule(before)
	oldRule, _, oldRD := storedRule(ss.s)
	// Only if the Series is still as loaded: a concurrent Split must not end it twice and start two new ones.
	res, err := tx.ExecContext(ctx, "UPDATE events SET rrule = ?, rdates_json = ?, updated_at = ? WHERE id = ? AND rrule IS ? AND rdates_json IS ?",
		rule, rd, a.Now().Unix(), ss.ev.id, oldRule, oldRD)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errSeriesChanged
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM events WHERE recurring_master_id = ? AND original_start_utc >= ?", ss.ev.id, o)
	return err
}

// retype is stored dates moved to an all-day or timed Series' form; clock is the timed one's HH:MM.
func retype(ds []string, allDay bool, clock string) []string {
	out := make([]string, len(ds))
	for i, d := range ds {
		out[i] = d[:len(dateLayout)]
		if !allDay {
			out[i] += "T" + clock
		}
	}
	return out
}
