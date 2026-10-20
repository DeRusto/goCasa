package app

import (
	"bytes"
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
)

// Google write-back (spec §7.2, Phase 2b): a change goes to Google first, with
// If-Match on the etag GoCasa last saw, and the local copy changes only
// through the sync straight after. If Google can't be reached, nothing
// changes: there is no queue. Members are GoCasa's own and never reach Google.

var (
	errUnreachable = &refusal{msg: "GoCasa couldn't reach Google, so nothing changed. Try again.", status: http.StatusBadGateway}
	errStale       = &refusal{msg: "This event changed in Google. This is its current version: redo your change.", conflict: true}
	errReadOnly    = &refusal{msg: "That Google calendar is read-only."}
	// An event synced before etags were kept has none to send as If-Match; the full re-import migration 0009 starts fills it.
	errNoVersion = &refusal{msg: "GoCasa hasn't synced this event's version from Google yet. Sync now, then try again.", conflict: true}
	// The write reached Google; the sync after it didn't, so GoCasa still shows the old copy.
	errNotLanded = &refusal{msg: "Saved in Google, but GoCasa couldn't sync it back yet: the calendar shows it after the next sync. Members picked for a new event need picking again then.", status: http.StatusBadGateway}
)

// googleRow is a Google event as a write needs it.
type googleRow struct {
	id, calendarID, account          int64
	calendar, external, series, etag string // Google's ids; series is "" unless an occurrence
	readOnly                         bool
}

// googleResult is what a write leaves for the sync after it: the Google id
// whose event or series rows the change's Members go on (only those from
// event row from on, if set), whether target is a Series (its later
// occurrences keep the choice), whether those rows are new (their Members start
// as the calendar's defaults), whether a series' rule changed, so its
// calendar re-imports in full, and whether Google was left alone (local), so
// there is nothing to sync.
type googleResult struct {
	target                     string
	from                       int64
	series, fresh, full, local bool
}

// googleAPI is a Google calendar's events, with an access token.
type googleAPI struct {
	a           *App
	access, cal string
}

// loadGoogleRow reads Google event id, or sql.ErrNoRows.
func (a *App) loadGoogleRow(ctx context.Context, id int64) (googleRow, error) {
	g := googleRow{id: id}
	err := a.DB.QueryRowContext(ctx, `SELECT e.calendar_id, i.id, c.external_id, e.external_id, coalesce(e.external_series_id, ''), coalesce(e.etag, ''), c.read_only
		FROM events e JOIN source_calendars c ON c.id = e.calendar_id JOIN integrations i ON i.id = c.integration_id
		WHERE e.id = ? AND i.type = ?`, id, googleType).Scan(&g.calendarID, &g.account, &g.calendar, &g.external, &g.series, &g.etag, &g.readOnly)
	return g, err
}

// googleCalendarAPI is the API for Google calendar cal of account, refusing
// one that needs reconnecting.
func (a *App) googleCalendarAPI(ctx context.Context, account int64, cal string) (googleAPI, error) {
	var enc []byte
	var reconnect bool
	if err := a.DB.QueryRowContext(ctx, "SELECT credentials_enc, status = 'reconnect_needed' FROM integrations WHERE id = ?", account).Scan(&enc, &reconnect); err != nil {
		return googleAPI{}, err
	}
	plain, err := a.unseal(enc)
	if err != nil {
		return googleAPI{}, err
	}
	var tok googleToken
	json.Unmarshal(plain, &tok)
	access := ""
	if !reconnect {
		access, err = a.googleAccess(ctx, tok.RefreshToken)
	}
	switch {
	case reconnect || errors.Is(err, errReconnect):
		return googleAPI{}, &refusal{msg: "Reconnect needed: an Admin reconnects this Google account in Settings. Nothing changed.", status: http.StatusBadGateway}
	case errors.As(err, new(transientError)):
		return googleAPI{}, errUnreachable
	case err != nil:
		return googleAPI{}, err
	}
	return googleAPI{a: a, access: access, cal: cal}, nil
}

// call sends method to event id ("" is the calendar's events, to insert),
// with If-Match etag unless "", body in as JSON and the answer decoded into out.
func (g googleAPI) call(ctx context.Context, method, id, etag string, in, out any) error {
	path := g.a.Upstreams.GoogleAPI + "/calendar/v3/calendars/" + url.PathEscape(g.cal) + "/events"
	if id != "" {
		path += "/" + url.PathEscape(id)
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.access)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if etag != "" {
		req.Header.Set("If-Match", etag)
	}
	res, err := googleClient.Do(req)
	if err != nil {
		g.a.Log.Warn("google write failed", "method", method, "err", err)
		return errUnreachable
	}
	defer res.Body.Close()
	switch {
	// Gone counts as changed: the reload shows it removed.
	case res.StatusCode == http.StatusPreconditionFailed || res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return errStale
	case res.StatusCode >= 500 || res.StatusCode == http.StatusTooManyRequests:
		return errUnreachable
	case res.StatusCode < 200 || res.StatusCode > 299:
		return &refusal{msg: "Google refused the change (" + res.Status + "), so nothing changed.", status: http.StatusBadGateway}
	case out != nil:
		if err := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 1<<20)).Decode(out); err != nil {
			return fmt.Errorf("Google's answer: %w", err)
		}
	}
	return nil
}

// googleBody is c as Google's event fields. The other kind of time is sent as
// null, so a PATCH turning a timed event all-day clears its dateTime.
func googleBody(c eventChange) map[string]any {
	b := map[string]any{"summary": c.title, "location": c.location, "description": c.description}
	if c.allDay {
		b["start"] = map[string]any{"date": c.date, "dateTime": nil, "timeZone": nil}
		b["end"] = map[string]any{"date": c.endDate, "dateTime": nil, "timeZone": nil}
		return b
	}
	tz := c.tzid.(string)
	zone, _ := time.LoadLocation(tz) // parseEventForm checked it
	for k, t := range map[string]any{"start": c.start, "end": c.end} {
		b[k] = map[string]any{"dateTime": time.Unix(t.(int64), 0).In(zone).Format(time.RFC3339), "timeZone": tz, "date": nil}
	}
	return b
}

// googleRecurrence is c's rule, EXDATEs and RDATEs as Google's recurrence lines.
func googleRecurrence(c eventChange) []string {
	rule, ok := c.rrule.(string)
	if !ok {
		return nil
	}
	lines := []string{"RRULE:" + rule}
	param, sec := ";VALUE=DATE:", ""
	if !c.allDay {
		param, sec = ";TZID="+c.tzid.(string)+":", "00"
	}
	basic := strings.NewReplacer("-", "", ":", "")
	for _, l := range [][2]string{{"EXDATE", c.exdates}, {"RDATE", c.rdates}} {
		var ds []string
		json.Unmarshal([]byte(l[1]), &ds)
		for _, d := range ds {
			lines = append(lines, l[0]+param+basic.Replace(d)+sec)
		}
	}
	return lines
}

// originalOf is a Google start as an Occurrence key: the instant, or for
// all-day the date's UTC midnight.
func originalOf(t googleTime) (int64, error) {
	if t.Date != "" {
		d, err := time.Parse(dateLayout, t.Date)
		return d.Unix(), err
	}
	d, err := time.Parse(time.RFC3339, t.DateTime)
	return d.Unix(), err
}

// splitRecurrence is master's recurrence ended just before its occurrence o,
// and the recurrence a new series from o takes: the same rule with an
// unchanged COUNT less the occurrences before o, its EXDATEs, and the RDATEs
// from o on, which the old series loses.
func (a *App) splitRecurrence(master googleEvent, o int64) (old, from []string, err error) {
	for _, line := range master.Recurrence {
		head, values, _ := strings.Cut(line, ":")
		switch {
		case strings.HasPrefix(line, "RDATE"):
			zone := tzidZone(head, a.seriesZone(master))
			var before, after []string
			for _, v := range strings.Split(values, ",") {
				if t, ok := recurInstant(v, zone); ok && t.Unix() >= o {
					after = append(after, v)
				} else {
					before = append(before, v)
				}
			}
			if len(before) > 0 {
				old = append(old, head+":"+strings.Join(before, ","))
			}
			if len(after) > 0 {
				from = append(from, head+":"+strings.Join(after, ","))
			}
			continue
		case strings.HasPrefix(line, "EXDATE"):
			old, from = append(old, line), append(from, line)
			continue
		case !strings.HasPrefix(line, "RRULE:"):
			old = append(old, line)
			continue
		}
		rule := values
		old = append(old, "RRULE:"+untilBefore(rule, o, master.Start.Date != ""))
		if countRE.MatchString(rule) {
			mc, ok := master.change(master.Start.TimeZone, a.Config.Location)
			if !ok {
				return nil, nil, errors.New("Google sent a series start GoCasa can't read")
			}
			ev, zone := calendarEvent{allDay: mc.allDay}, (*time.Location)(nil)
			if mc.allDay {
				ev.date, ev.endDate = mc.date.(string), mc.endDate.(string)
			} else {
				ev.start, ev.end = mc.start.(int64), mc.end.(int64)
				zone, _ = time.LoadLocation(mc.tzid.(string))
			}
			to := time.Unix(o, 0).UTC()
			first := time.Unix(ev.start, 0)
			if ev.allDay {
				first, _ = time.Parse(dateLayout, ev.date)
			}
			if to.After(first.AddDate(horizonYears, 0, 0)) {
				return nil, nil, &refusal{msg: fmt.Sprintf("GoCasa counts a Series' events only %d years from its start: split this one in Google.", horizonYears)}
			}
			_, after, err := split(ev, series{rule: rule}, zone, o)
			if err != nil {
				return nil, nil, err
			}
			rule = after.rule
		}
		from = append(from, "RRULE:"+rule)
	}
	return old, from, nil
}

// shiftDates is recurrence lines with their EXDATEs and RDATEs moved as a
// Split moves Series master's Occurrence o to c's start, in c's kind of time
// and zone, so they keep naming the same Occurrences.
func (a *App) shiftDates(lines []string, master googleEvent, o int64, c eventChange) []string {
	oldZone := a.seriesZone(master)
	// Wall times as UTC (see floating), so a shift keeps 10:00 at 10:00 across DST; all-day keys are midnights already.
	oldWall := time.Unix(o, 0).UTC()
	if master.Start.Date == "" {
		oldWall = floating(oldWall.In(oldZone))
	}
	param, layout := ";VALUE=DATE:", "20060102"
	newWall, _ := time.Parse(dateLayout, fmt.Sprint(c.date))
	if !c.allDay {
		zone, _ := time.LoadLocation(c.tzid.(string))
		newWall = floating(time.Unix(c.start.(int64), 0).In(zone))
		param, layout = ";TZID="+c.tzid.(string)+":", "20060102T150405"
	}
	delta := newWall.Sub(oldWall)
	var out []string
	for _, line := range lines {
		name, _, _ := strings.Cut(line, ";")
		if name != "EXDATE" && name != "RDATE" {
			out = append(out, line)
			continue
		}
		head, values, _ := strings.Cut(line, ":")
		zone := tzidZone(head, oldZone)
		var vs []string
		for _, v := range strings.Split(values, ",") {
			t, err := time.Parse("20060102T150405", v)
			switch {
			case strings.HasSuffix(v, "Z"):
				t, err = time.Parse("20060102T150405Z", v)
				t = floating(t.In(zone))
			case len(v) == len("20060102"):
				t, err = time.Parse("20060102", v)
			}
			if err == nil {
				vs = append(vs, t.Add(delta).Format(layout))
			}
		}
		if len(vs) > 0 {
			out = append(out, name+param+strings.Join(vs, ","))
		}
	}
	return out
}

// seriesZone is Google Series master's own zone, else the Household's.
func (a *App) seriesZone(master googleEvent) *time.Location {
	if z, err := time.LoadLocation(master.Start.TimeZone); master.Start.TimeZone != "" && err == nil {
		return z
	}
	return a.Config.Location
}

// tzidZone is the zone a recurrence line's head ("EXDATE;TZID=…") names, else zone.
func tzidZone(head string, zone *time.Location) *time.Location {
	if _, tz, ok := strings.Cut(head, "TZID="); ok {
		if z, err := time.LoadLocation(strings.Split(tz, ";")[0]); err == nil {
			return z
		}
	}
	return zone
}

// recurInstant reads an EXDATE or RDATE value as an Occurrence key: UTC, a
// wall time in zone, or a date's UTC midnight.
func recurInstant(v string, zone *time.Location) (time.Time, bool) {
	if t, err := time.Parse("20060102T150405Z", v); err == nil {
		return t, true
	}
	if t, err := time.Parse("20060102", v); err == nil {
		return t, true
	}
	t, err := time.Parse("20060102T150405", v)
	if err == nil {
		t, err = parseLocal(t.Format(localLayout), zone)
	}
	return t, err == nil
}

// untilBefore is rule ending just before Occurrence o. Its own COUNT or UNTIL
// went past o, else o wouldn't exist, and RFC 5545 allows only one of them.
func untilBefore(rule string, o int64, allDay bool) string {
	var parts []string
	for _, p := range strings.Split(rule, ";") {
		if k, _, _ := strings.Cut(p, "="); k != "UNTIL" && k != "COUNT" {
			parts = append(parts, p)
		}
	}
	until := time.Unix(o-1, 0).UTC().Format("20060102T150405Z")
	if allDay { // UNTIL takes DTSTART's type
		until = time.Unix(o, 0).UTC().AddDate(0, 0, -1).Format("20060102")
	}
	return strings.Join(append(parts, "UNTIL="+until), ";")
}

// seriesOf reads row's Series from Google and the key o of row's Occurrence,
// refusing as stale an Occurrence that changed since GoCasa last saw it.
// first is whether o is the Series' first.
// shortcut: the Series' etag is the one just read, so a Series change that left this Occurrence alone isn't caught; store Series etags if that bites.
func (g googleAPI) seriesOf(ctx context.Context, row googleRow) (master googleEvent, o int64, first bool, err error) {
	var occ googleEvent
	if err := g.call(ctx, http.MethodGet, row.external, "", nil, &occ); err != nil {
		return master, 0, false, err
	}
	if occ.Etag != row.etag {
		return master, 0, false, errStale
	}
	if err := g.call(ctx, http.MethodGet, row.series, "", nil, &master); err != nil {
		return master, 0, false, err
	}
	o, err0 := originalOf(occ.OriginalStartTime)
	m, err1 := originalOf(master.Start)
	if err := errors.Join(err0, err1); err != nil {
		return master, 0, false, fmt.Errorf("Google's series times: %w", err)
	}
	return master, o, o == m, nil
}

// writeGoogle makes change c to Google event row, which stands as was, in
// Edit scope sc (spec §7.2): This event that occurrence, All events the
// series' text, This and following a Split, or on the first occurrence the
// whole series. A Split creates the new series, then ends the old one with
// UNTIL, deleting the new one if that fails.
func (a *App) writeGoogle(ctx context.Context, row googleRow, sc scope, c, was eventChange) (googleResult, error) {
	if row.readOnly {
		return googleResult{}, errReadOnly
	}
	if row.series != "" && sc == scopeAll && (c.allDay != was.allDay || shownTimes(c) != shownTimes(was)) {
		return googleResult{}, &refusal{msg: "All events changes only the title, location, description and Members; change times with This event or This and following."}
	}
	if row.series != "" && sc != scopeThis && sc != scopeAll && sc != scopeFollowing {
		return googleResult{}, &refusal{msg: "Pick which events to change."}
	}
	if c.title == was.title && c.location == was.location && c.description == was.description && c.allDay == was.allDay && shownTimes(c) == shownTimes(was) {
		// Only the Members, which are GoCasa's own: Google isn't asked.
		switch {
		case row.series == "" || sc == scopeThis:
			return googleResult{target: row.external, local: true}, nil
		case sc == scopeFollowing:
			return googleResult{target: row.series, series: true, from: row.id, local: true}, nil
		}
		return googleResult{target: row.series, series: true, local: true}, nil
	}
	if row.etag == "" {
		return googleResult{}, errNoVersion
	}
	g, err := a.googleCalendarAPI(ctx, row.account, row.calendar)
	if err != nil {
		return googleResult{}, err
	}
	if row.series == "" || sc == scopeThis {
		return googleResult{target: row.external}, g.call(ctx, http.MethodPatch, row.external, row.etag, googleBody(c), nil)
	}
	master, o, first, err := g.seriesOf(ctx, row)
	if err != nil {
		return googleResult{}, err
	}
	switch {
	case sc == scopeAll:
		// Only the fields changed from the occurrence apply, so editing from an edited one doesn't spread its own changes.
		patch := map[string]any{}
		for _, f := range [][3]string{{"summary", c.title, was.title}, {"location", c.location, was.location}, {"description", c.description, was.description}} {
			if f[1] != f[2] {
				patch[f[0]] = f[1]
			}
		}
		if len(patch) > 0 {
			err = g.call(ctx, http.MethodPatch, row.series, master.Etag, patch, nil)
		}
		return googleResult{target: row.series, series: true}, err
	case first: // a new start renames every occurrence, so re-import
		return googleResult{target: row.series, series: true, full: true}, g.call(ctx, http.MethodPatch, row.series, master.Etag, googleBody(c), nil)
	}
	old, from, err := a.splitRecurrence(master, o)
	if err != nil {
		return googleResult{}, err
	}
	body := googleBody(c)
	body["recurrence"] = a.shiftDates(from, master, o, c)
	var made googleEvent
	if err := g.call(ctx, http.MethodPost, "", "", body, &made); err != nil {
		return googleResult{}, err
	}
	if err := g.call(ctx, http.MethodPatch, row.series, master.Etag, map[string]any{"recurrence": old}, nil); err != nil {
		if derr := g.call(context.WithoutCancel(ctx), http.MethodDelete, made.ID, "", nil, nil); derr != nil {
			a.Log.Error("a split's new google series outlived its failure", "calendar", row.calendarID, "series", made.ID, "err", derr)
			return googleResult{full: true}, &refusal{msg: "GoCasa made the new Series in Google but couldn't end the old one or take the new one back: fix the overlap in Google.", status: http.StatusBadGateway}
		}
		return googleResult{}, err
	}
	return googleResult{target: made.ID, series: true, fresh: true, full: true}, nil
}

// removeGoogle deletes Google event row in Edit scope sc: This event cancels
// that occurrence, This and following ends the series before it (deleting it
// from its first occurrence), All events deletes the series.
func (a *App) removeGoogle(ctx context.Context, row googleRow, sc scope) (googleResult, error) {
	switch {
	case row.readOnly:
		return googleResult{}, errReadOnly
	case row.series != "" && sc != scopeThis && sc != scopeAll && sc != scopeFollowing:
		return googleResult{}, &refusal{msg: "Pick which events to delete."}
	case row.etag == "":
		return googleResult{}, errNoVersion
	}
	g, err := a.googleCalendarAPI(ctx, row.account, row.calendar)
	if err != nil {
		return googleResult{}, err
	}
	if row.series == "" || sc == scopeThis {
		return googleResult{}, g.call(ctx, http.MethodDelete, row.external, row.etag, nil, nil)
	}
	master, o, first, err := g.seriesOf(ctx, row)
	if err != nil {
		return googleResult{}, err
	}
	if sc == scopeAll || first {
		return googleResult{}, g.call(ctx, http.MethodDelete, row.series, master.Etag, nil, nil)
	}
	old, _, err := a.splitRecurrence(master, o)
	if err != nil {
		return googleResult{}, err
	}
	return googleResult{full: true}, g.call(ctx, http.MethodPatch, row.series, master.Etag, map[string]any{"recurrence": old}, nil)
}

// landGoogle runs the sync after a write to calendar cal of account, giving
// res' rows Members members unless they already have them (base) — and
// returns once it's done, so the page that follows shows the change.
// It is errNotLanded if that sync failed.
func (a *App) landGoogle(ctx context.Context, account, cal int64, res googleResult, members, base []int64) (err error) {
	if res.full {
		// Under the sync's lock: a sync running now would write back the token it read.
		mu, _ := a.syncLocks.LoadOrStore(account, &sync.Mutex{})
		mu.(*sync.Mutex).Lock()
		_, err := a.DB.ExecContext(ctx, `UPDATE integrations SET sync_cursor = json_remove(sync_cursor, '$.tokens.' || json_quote((SELECT external_id FROM source_calendars WHERE id = ?)))
			WHERE id = ? AND json_valid(sync_cursor)`, cal, account)
		mu.(*sync.Mutex).Unlock()
		if err != nil {
			return err
		}
	}
	landed := true
	if !res.local {
		if err := a.syncIntegration(ctx, account); err != nil {
			return err
		}
		var failures int
		if err := a.DB.QueryRowContext(ctx, "SELECT consecutive_failures FROM integrations WHERE id = ?", account).Scan(&failures); err != nil {
			return err
		}
		landed = failures == 0
	}
	if !landed {
		defer func() { err = cmp.Or(err, error(errNotLanded)) }()
	}
	if res.target == "" {
		return nil
	}
	active, err := a.activeMemberIDs(ctx)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(active, func(m int64) bool { return slices.Contains(members, m) != slices.Contains(base, m) }) {
		return nil
	}
	var ids []int64
	err = a.inTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `UPDATE events SET local_override = 1 WHERE calendar_id = ?1 AND (external_id = ?2 OR external_series_id = ?2)
				AND (?3 = 0 OR `+startKey+` >= (SELECT `+startKey+` FROM events WHERE id = ?3))
			RETURNING id`, cal, res.target, res.from)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, id := range ids {
			if err := setMembers(ctx, tx, id, members); err != nil {
				return err
			}
		}
		if !res.series {
			return nil
		}
		// Kept for the Series' occurrences that sync later, even with none in the window yet.
		if _, err := tx.ExecContext(ctx, `DELETE FROM google_series_members WHERE calendar_id = ?1 AND series_id = ?2
			AND (?3 = 0 OR from_key >= (SELECT `+startKey+` FROM events WHERE id = ?3))`, cal, res.target, res.from); err != nil {
			return err
		}
		list, _ := json.Marshal(members)
		_, err = tx.ExecContext(ctx, `INSERT INTO google_series_members (calendar_id, series_id, from_key, members)
			VALUES (?1, ?2, (SELECT `+startKey+` FROM events WHERE id = ?3), ?4)`, cal, res.target, res.from, string(list))
		return err
	})
	if err == nil && len(ids) > 0 {
		a.publish("updated", "event", ids[0])
	}
	return err
}

// setMembers gives event id Members members. The form lists only active
// Members, so an archived one stays on the event.
func setMembers(ctx context.Context, tx *sql.Tx, id int64, members []int64) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM event_members WHERE event_id = ? AND member_id IN (SELECT id FROM members WHERE NOT archived)", id); err != nil {
		return err
	}
	for _, m := range members {
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO event_members (event_id, member_id) SELECT ?, id FROM members WHERE id = ? AND NOT archived", id, m); err != nil {
			return err
		}
	}
	return nil
}

// googlePage shows Google event id in the event dialog as GoCasa has it now.
func (a *App) googlePage(w http.ResponseWriter, r *http.Request, status int, id int64, msg string) {
	row, err := a.loadGoogleRow(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "That event was removed in Google.", http.StatusNotFound)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	was, err := a.occurrenceNow(r.Context(), found{ss: storedSeries{ev: calendarEvent{id: id}}})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	f, err := a.eventForm(was)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	f.ID, f.Synced, f.InSeries, f.Scope, f.Etag = id, true, row.series != "", string(scopeThis), row.etag
	a.eventPage(w, r, status, f, msg)
}

// googleAnswer answers a Google write that ended in err: a stale one reloads
// the event and shows its current version, another refusal shows form f.
func (a *App) googleAnswer(w http.ResponseWriter, r *http.Request, row googleRow, f web.EventForm, err error) {
	var no *refusal
	switch {
	case errors.Is(err, errStale):
		msg := errStale.msg
		if err := a.landGoogle(r.Context(), row.account, row.calendarID, googleResult{}, nil, nil); errors.Is(err, errNotLanded) {
			msg = "This event changed in Google, and GoCasa couldn't reload it: Sync now, then redo your change."
		} else if err != nil {
			a.serverError(w, r, err)
			return
		}
		a.googlePage(w, r, http.StatusConflict, row.id, msg)
	case errors.As(err, &no):
		a.eventPage(w, r, no.code(), f, no.msg)
	default:
		a.serverError(w, r, err)
	}
}

// landFailed answers a change whose landing failed: errNotLanded tells the
// person it reached Google; anything else is a server error.
func (a *App) landFailed(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errNotLanded) {
		http.Error(w, errNotLanded.msg, errNotLanded.code())
		return
	}
	a.serverError(w, r, err)
}

// saveGoogle changes a Google event from the dialog, in the Edit scope it picks.
func (a *App) saveGoogle(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	row, err := a.loadGoogleRow(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	f, c, msg := a.parseEventForm(r, false)
	sc := scope(cmp.Or(r.PostFormValue("scope"), string(scopeThis)))
	f.ID, f.CalendarID, f.Synced, f.InSeries, f.Scope, f.Etag = id, row.calendarID, true, row.series != "", string(sc), r.PostFormValue("etag")
	if f.Etag != row.etag { // a sync since the dialog opened brought a newer version
		a.googleAnswer(w, r, row, f, errStale)
		return
	}
	if msg != "" {
		a.eventPage(w, r, http.StatusBadRequest, f, msg)
		return
	}
	was, err := a.occurrenceNow(r.Context(), found{ss: storedSeries{ev: calendarEvent{id: id}}})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	res, err := a.writeGoogle(r.Context(), row, sc, c, was)
	if err != nil {
		a.googleAnswer(w, r, row, f, err)
		return
	}
	base := was.members
	if res.fresh {
		if base, err = a.defaultMembers(r.Context(), row.calendarID); err != nil {
			a.serverError(w, r, err)
			return
		}
	}
	if err := a.landGoogle(r.Context(), row.account, row.calendarID, res, c.members, base); err != nil {
		a.landFailed(w, r, err)
		return
	}
	http.Redirect(w, r, "/calendar?date="+c.firstDay, http.StatusSeeOther)
}

// deleteGoogle deletes a Google event, in the Edit scope the form picks.
func (a *App) deleteGoogle(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	row, err := a.loadGoogleRow(r.Context(), id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	res, err := googleResult{}, error(errStale) // a sync since the dialog opened brought a newer version
	if r.PostFormValue("etag") == row.etag {
		res, err = a.removeGoogle(r.Context(), row, scope(cmp.Or(r.PostFormValue("scope"), string(scopeThis))))
	}
	var no *refusal
	switch {
	case errors.Is(err, errStale):
		a.googleAnswer(w, r, row, web.EventForm{}, err)
	case errors.As(err, &no):
		a.googlePage(w, r, no.code(), id, no.msg)
	case err != nil:
		a.serverError(w, r, err)
	default:
		if err := a.landGoogle(r.Context(), row.account, row.calendarID, res, nil, nil); err != nil {
			a.landFailed(w, r, err)
			return
		}
		http.Redirect(w, r, "/calendar", http.StatusSeeOther)
	}
}

// createGoogle creates event c from the dialog f if its calendar is a Google
// one; ok is false, with nothing answered, if it isn't.
func (a *App) createGoogle(w http.ResponseWriter, r *http.Request, f web.EventForm, c eventChange) (ok bool) {
	var account int64
	var cal string
	var readOnly bool
	err := a.DB.QueryRowContext(r.Context(), `SELECT i.id, c.external_id, c.read_only FROM source_calendars c JOIN integrations i ON i.id = c.integration_id
		WHERE c.id = ? AND i.type = ? AND c.enabled`, c.calendarID, googleType).Scan(&account, &cal, &readOnly)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	row := googleRow{calendarID: c.calendarID, account: account, calendar: cal}
	var made googleEvent
	if err == nil && readOnly {
		err = errReadOnly
	}
	if err == nil {
		var g googleAPI
		if g, err = a.googleCalendarAPI(r.Context(), account, cal); err == nil {
			body := googleBody(c)
			if rec := googleRecurrence(c); rec != nil {
				body["recurrence"] = rec
			}
			if err = g.call(r.Context(), http.MethodPost, "", "", body, &made); errors.Is(err, errStale) {
				err = &refusal{msg: "Google has no such calendar any more, so nothing changed.", status: http.StatusBadGateway}
			}
		}
	}
	var base []int64 // none picked: the dialog doesn't know the defaults, so they apply
	if err == nil && len(c.members) > 0 {
		base, err = a.defaultMembers(r.Context(), c.calendarID)
	}
	if err == nil {
		err = a.landGoogle(r.Context(), account, c.calendarID, googleResult{target: made.ID, series: googleRecurrence(c) != nil, fresh: true}, c.members, base)
		if err != nil {
			a.landFailed(w, r, err)
			return true
		}
		http.Redirect(w, r, "/calendar?date="+c.firstDay, http.StatusSeeOther)
		return true
	}
	a.googleAnswer(w, r, row, f, err)
	return true
}

// defaultMembers is calendar cal's default Members.
func (a *App) defaultMembers(ctx context.Context, cal int64) ([]int64, error) {
	rows, err := a.DB.QueryContext(ctx, "SELECT member_id FROM source_calendar_members WHERE calendar_id = ?", cal)
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
