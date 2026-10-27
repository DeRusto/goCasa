package app

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
)

// googleSync syncs a Google account's shown calendars (spec §7.2): Google's
// own occurrences (singleEvents), kept from 3 months back to 12 months ahead.
// Incremental syncs use each calendar's syncToken; a daily full re-import at a
// random time, and any 410, re-lists the window and drops what Google lacks.
type googleSync struct{}

const (
	googleSyncInterval = 120 // seconds
	maxGooglePages     = 100
)

var (
	errReconnect    = errors.New("Google withdrew GoCasa's access: reconnect the account in Settings")
	errGoogleGone   = errors.New("Google expired the sync token")
	errCalendarGone = errors.New("Google has no such calendar for this account")
)

// googleCursor is a Google account's sync_cursor.
type googleCursor struct {
	NextFull int64             `json:"next_full"` // when the daily full re-import is due
	Tokens   map[string]string `json:"tokens"`    // a calendar's Google id → its syncToken
}

type googleEvent struct {
	ID                string     `json:"id"`
	Etag              string     `json:"etag"`
	Status            string     `json:"status"`
	Summary           string     `json:"summary"`
	Description       string     `json:"description"`
	Location          string     `json:"location"`
	RecurringEventID  string     `json:"recurringEventId"`
	Start             googleTime `json:"start"`
	End               googleTime `json:"end"`
	OriginalStartTime googleTime `json:"originalStartTime"` // an occurrence's
	Recurrence        []string   `json:"recurrence"`        // a series' RRULE, EXDATE and RDATE lines
}

type googleTime struct {
	Date     string `json:"date"`
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone"`
}

// syncWindow is the span of Google events GoCasa keeps.
func syncWindow(now time.Time) (from, to time.Time) {
	return now.AddDate(0, -3, 0), now.AddDate(0, 12, 0)
}

func (googleSync) Sync(ctx context.Context, a *App, in integrationRow) error {
	var tok googleToken
	json.Unmarshal(in.credentials, &tok)
	access, err := a.googleAccess(ctx, tok.RefreshToken)
	if errors.Is(err, errReconnect) {
		// Unless reconnected meanwhile: then this refusal was of the old token.
		if uerr := a.integrations.NeedsReconnect(ctx, in.id, in.sealed); uerr != nil {
			return uerr
		}
	}
	if err != nil {
		return err
	}
	var cur googleCursor
	json.Unmarshal([]byte(in.cursor), &cur) // "" is no cursor yet
	if cur.Tokens == nil {
		cur.Tokens = map[string]string{}
	}
	now := a.Now()
	full := now.Unix() >= cur.NextFull
	rows, err := a.DB.QueryContext(ctx, "SELECT id, external_id FROM source_calendars WHERE integration_id = ? AND enabled ORDER BY id", in.id)
	if err != nil {
		return err
	}
	type calendar struct {
		id       int64
		external string
	}
	var cals []calendar
	for rows.Next() {
		var c calendar
		if err := rows.Scan(&c.id, &c.external); err != nil {
			rows.Close()
			return err
		}
		cals = append(cals, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, c := range cals {
		token := cur.Tokens[c.external]
		if full {
			token = ""
		}
		items, zone, next, err := a.googleEvents(ctx, access, c.external, token, now)
		if errors.Is(err, errGoogleGone) {
			token = ""
			items, zone, next, err = a.googleEvents(ctx, access, c.external, "", now)
		}
		if errors.Is(err, errCalendarGone) {
			// Removed or unshared in Google: hide it, keeping its events and Members, and sync the rest.
			a.Log.Warn("hiding a google calendar google no longer has", "calendar", c.id)
			if _, err := a.exec(ctx, web.KindEvent, "UPDATE source_calendars SET enabled = 0 WHERE id = ?", c.id); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		cur.Tokens[c.external] = next
		cursor, _ := json.Marshal(cur)
		if err := a.storeGoogleEvents(ctx, in.id, c.id, items, zone, token == "", string(cursor), now); err != nil {
			return err
		}
	}
	if full {
		// A random time tomorrow, so Households don't all re-import at once.
		cur.NextFull = dayAt(now.In(a.location(ctx)), 1).Add(rand.N(24 * time.Hour)).Unix()
		cursor, _ := json.Marshal(cur)
		_, err = a.DB.ExecContext(ctx, "UPDATE integrations SET sync_cursor = ? WHERE id = ?", string(cursor), in.id)
	}
	return err
}

// googleAccess trades a refresh token for an access token. An invalid_grant
// is errReconnect; anything else may pass.
func (a *App) googleAccess(ctx context.Context, refresh string) (string, error) {
	var body struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	status, err := a.googlePost(ctx, "/token", url.Values{
		"client_id":     {a.Config.GoogleClientID},
		"client_secret": {a.Config.GoogleClientSecret},
		"refresh_token": {refresh},
		"grant_type":    {"refresh_token"},
	}, &body)
	switch {
	case err != nil:
		return "", transient(err)
	case body.Error == "invalid_grant":
		return "", errReconnect
	case status != http.StatusOK || body.AccessToken == "":
		return "", byStatus(status, fmt.Errorf("Google wouldn't refresh access (%d %s)", status, body.Error))
	}
	return body.AccessToken, nil
}

// googleEvents lists calendar cal's events, every page: the changes since
// token, or with no token the window around now. It returns the calendar's
// zone and the token for next time; an expired token is errGoogleGone.
func (a *App) googleEvents(ctx context.Context, access, cal, token string, now time.Time) (items []googleEvent, zone, next string, err error) {
	// singleEvents on every request: a token sync must ask what the full one did.
	q := url.Values{"singleEvents": {"true"}, "maxResults": {"2500"}}
	if token != "" {
		q.Set("syncToken", token)
	} else {
		from, to := syncWindow(now)
		q.Set("timeMin", from.UTC().Format(time.RFC3339))
		q.Set("timeMax", to.UTC().Format(time.RFC3339))
	}
	sc := syncClient{name: "Google", client: googleClient, header: http.Header{"Authorization": {"Bearer " + access}}, limit: 32 << 20,
		overrides: map[int]error{http.StatusGone: errGoogleGone, http.StatusNotFound: errCalendarGone}}
	for range maxGooglePages {
		var body struct {
			Items         []googleEvent `json:"items"`
			NextPageToken string        `json:"nextPageToken"`
			NextSyncToken string        `json:"nextSyncToken"`
			TimeZone      string        `json:"timeZone"`
		}
		if err := sc.json(ctx, http.MethodGet, a.Upstreams.GoogleAPI+"/calendar/v3/calendars/"+url.PathEscape(cal)+"/events?"+q.Encode(), nil, &body); err != nil {
			return nil, "", "", err
		}
		items = append(items, body.Items...)
		if body.NextPageToken == "" {
			return items, body.TimeZone, body.NextSyncToken, nil
		}
		q.Set("pageToken", body.NextPageToken)
	}
	return nil, "", "", fmt.Errorf("Google sent more than %d pages of events", maxGooglePages)
}

// storeGoogleEvents applies items to calendar cal of integration id, with its
// next cursor, in one transaction. A full listing also deletes what it left
// out. Events outside the window are dropped, whether sent or not.
func (a *App) storeGoogleEvents(ctx context.Context, id, cal int64, items []googleEvent, calZone string, full bool, cursor string, now time.Time) error {
	from, to := syncWindow(now)
	home := a.location(ctx)
	changed := len(items) > 0 // a token sync sends only what changed
	return a.inTxIf(ctx, &changed, func(tx *sql.Tx) error {
		seen := []string{}
		var upserted []int64
		for _, e := range items {
			if e.Status == "cancelled" {
				// A whole series' deletion may come as its own id alone.
				if _, err := tx.ExecContext(ctx, "DELETE FROM events WHERE calendar_id = ?1 AND (external_id = ?2 OR external_series_id = ?2)", cal, e.ID); err != nil {
					return err
				}
				if err := forgetSeriesMembers(ctx, tx, cal, e.ID); err != nil {
					return err
				}
				continue
			}
			seen = append(seen, e.ID)
			c, ok := e.change(cmp.Or(e.Start.TimeZone, calZone), home)
			if !ok {
				continue // its last good copy stays
			}
			var row int64
			err := tx.QueryRowContext(ctx, `INSERT INTO events (calendar_id, external_id, external_series_id, title, location, description, all_day,
					start_utc, end_utc, start_tzid, all_day_date, all_day_end_date, etag, updated_at)
				VALUES (?, ?, nullif(?, ''), ?, ?, ?, ?, ?, ?, ?, ?, ?, nullif(?, ''), ?)
				ON CONFLICT (calendar_id, external_id) WHERE external_id IS NOT NULL DO UPDATE SET external_series_id = excluded.external_series_id,
					title = excluded.title, location = excluded.location, description = excluded.description, all_day = excluded.all_day,
					start_utc = excluded.start_utc, end_utc = excluded.end_utc, start_tzid = excluded.start_tzid,
					all_day_date = excluded.all_day_date, all_day_end_date = excluded.all_day_end_date, etag = excluded.etag, status = 'confirmed', updated_at = excluded.updated_at
				RETURNING id`,
				cal, e.ID, e.RecurringEventID, c.title, c.location, c.description, c.allDay, c.start, c.end, c.tzid, c.date, c.endDate, e.Etag, now.Unix()).Scan(&row)
			if err != nil {
				return err
			}
			upserted = append(upserted, row)
		}
		if len(upserted) > 0 {
			if err := resolveMembers(ctx, tx, cal, upserted); err != nil {
				return err
			}
		}
		if full {
			keys, _ := json.Marshal(seen)
			res, err := tx.ExecContext(ctx, "DELETE FROM events WHERE calendar_id = ? AND external_id NOT IN (SELECT value FROM json_each(?))", cal, string(keys))
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			changed = changed || n > 0
		}
		fromDate, toDate := from.In(home).Format(dateLayout), to.In(home).Format(dateLayout)
		res, err := tx.ExecContext(ctx, `DELETE FROM events WHERE calendar_id = ?1
			AND (NOT all_day AND (end_utc <= ?2 OR start_utc >= ?3) OR all_day AND (all_day_end_date <= ?4 OR all_day_date >= ?5))`,
			cal, from.Unix(), to.Unix(), fromDate, toDate)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected() // events that left the window
		changed = changed || n > 0
		_, err = tx.ExecContext(ctx, "UPDATE integrations SET sync_cursor = ? WHERE id = ?", cursor, id)
		return err
	}, web.KindEvent)
}

// startKey is an events row's start as a number to compare: the instant, or for all-day the date's UTC midnight.
const startKey = "coalesce(start_utc, unixepoch(all_day_date))"

// change is e in its stored form, timed ones in zone (else home). ok is false
// if Google sent times it can't read.
func (e googleEvent) change(zone string, home *time.Location) (c eventChange, ok bool) {
	clip := func(s string, n int) string {
		r := []rune(strings.TrimSpace(s))
		return string(r[:min(len(r), n)])
	}
	c = eventChange{title: cmp.Or(clip(e.Summary, 200), "(No title)"), location: clip(e.Location, 200), description: clip(e.Description, 5000), exdates: "[]", rdates: "[]"}
	if e.Start.Date != "" {
		d0, err0 := time.Parse(dateLayout, e.Start.Date)
		d1, err1 := time.Parse(dateLayout, e.End.Date)
		if err0 != nil || err1 != nil {
			return c, false
		}
		c.allDay, c.date, c.endDate = true, e.Start.Date, e.End.Date
		if !d1.After(d0) {
			c.endDate = d0.AddDate(0, 0, 1).Format(dateLayout)
		}
		return c, true
	}
	t0, err0 := time.Parse(time.RFC3339, e.Start.DateTime)
	t1, err1 := time.Parse(time.RFC3339, e.End.DateTime)
	if err0 != nil || err1 != nil {
		return c, false
	}
	loc := home
	if zone != "" { // LoadLocation("") is UTC
		if z, err := time.LoadLocation(zone); err == nil {
			loc = z
		}
	}
	c.start, c.end, c.tzid = t0.Unix(), max(t1.Unix(), t0.Unix()+1), loc.String() // the events CHECK needs end after start
	return c, true
}

// syncNotices is the calendar page's notices: shown calendars whose sync has
// missed 3 times or needs a reconnect, and whether a view ending at end reaches
// past the Google window while a Google calendar is shown.
func (a *App) syncNotices(ctx context.Context, end time.Time) (stale []web.StaleCalendar, beyond bool, err error) {
	loc, clock := a.location(ctx), a.clock(ctx)
	rows, err := a.DB.QueryContext(ctx, `SELECT c.name, i.last_success_at, `+reconnectSQL("i")+`
		FROM source_calendars c JOIN integrations i ON i.id = c.integration_id
		WHERE c.enabled AND i.type != 'local' AND (`+reconnectSQL("i")+` OR `+downSQL("i")+`) ORDER BY c.name, c.id`)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var s web.StaleCalendar
		var ok sql.NullInt64
		if err := rows.Scan(&s.Name, &ok, &s.Reconnect); err != nil {
			return nil, false, err
		}
		s.LastUpdated = lastSync(ok, loc, clock)
		stale = append(stale, s)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if _, to := syncWindow(a.Now()); end.After(to) {
		err = a.DB.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM source_calendars c JOIN integrations i ON i.id = c.integration_id WHERE c.enabled AND i.type = ?)", googleType).Scan(&beyond)
	}
	return stale, beyond, err
}

// googleEventRows limits a query on events e to Google calendars' events.
const googleEventRows = "e.calendar_id IN (SELECT c.id FROM source_calendars c JOIN integrations i ON i.id = c.integration_id WHERE i.type = '" + googleType + "')"

// getSynced shows a Google event: in the event dialog, or on a read-only
// calendar with only its Members to change.
func (a *App) getSynced(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if row, err := a.loadGoogleRow(r.Context(), id); err == nil && !row.readOnly {
		a.googlePage(w, r, http.StatusOK, id, "")
		return
	}
	s := web.SyncedEvent{ID: id, Members: map[int64]bool{}}
	var allDay bool
	var start, end sql.NullInt64
	var date, endDate, members string
	err := a.DB.QueryRowContext(r.Context(), `SELECT e.title, e.all_day, e.start_utc, e.end_utc, coalesce(e.all_day_date, ''), coalesce(e.all_day_end_date, ''),
			(SELECT json_group_array(member_id) FROM event_members WHERE event_id = e.id)
		FROM events e WHERE e.id = ? AND `+googleEventRows, id).Scan(&s.Title, &allDay, &start, &end, &date, &endDate, &members)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	var ids []int64
	json.Unmarshal([]byte(members), &ids)
	for _, m := range ids {
		s.Members[m] = true
	}
	if allDay {
		d0, _ := time.Parse(dateLayout, date)
		d1, _ := time.Parse(dateLayout, endDate)
		s.When = d0.Format("2 Jan 2006")
		if last := d1.AddDate(0, 0, -1); last.After(d0) {
			s.When += " – " + last.Format("2 Jan 2006")
		}
	} else {
		loc, layout := a.location(r.Context()), "2 Jan 2006 "+a.clock(r.Context())
		s.When = time.Unix(start.Int64, 0).In(loc).Format(layout) + " – " + time.Unix(end.Int64, 0).In(loc).Format(layout)
	}
	ms, err := a.activeMembers(r)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, web.SyncedEventPage(s, ms))
}

// saveSynced sets a Google event's Members. It marks them a GoCasa change,
// which later syncs keep and which is never written back.
func (a *App) saveSynced(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := r.ParseForm(); err != nil { // a cut-off form mustn't clear the Members
		http.Error(w, "That form didn't arrive whole. Try again.", http.StatusBadRequest)
		return
	}
	err := a.inTx(r.Context(), func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(r.Context(), "SELECT e.id FROM events AS e WHERE e.id = ? AND "+googleEventRows, id).Scan(&id); err != nil {
			return err
		}
		return pickMembers(r.Context(), tx, []int64{id}, parseIDs(r.PostForm["members"]))
	}, web.KindEvent)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	case err != nil:
		a.serverError(w, r, err)
	default:
		http.Redirect(w, r, "/calendar", http.StatusSeeOther)
	}
}
