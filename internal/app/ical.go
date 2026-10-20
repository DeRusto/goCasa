package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/teambition/rrule-go"
)

// icalFeed syncs one read-only iCal/webcal feed (spec §7.3) into its one
// calendar. Its credentials are the feed URL, which may carry a secret token.
type icalFeed struct{}

const maxFeedBytes = 10 << 20

var feedClient = &http.Client{Timeout: time.Minute}

// syncFault runs after each event a feed sync writes; tests make it fail mid-transaction.
var syncFault = func(n int) {}

// feedCursor is an iCal feed's sync_cursor: what to send back on the next fetch.
type feedCursor struct {
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
}

// feedEvent is one VEVENT, in its stored form.
type feedEvent struct {
	uid       string
	original  int64 // a RECURRENCE-ID override's Occurrence (see expand); 0 for a single event or Series
	cancelled bool
	c         eventChange
}

// feedURL is u as fetched: webcal is https.
func feedURL(u string) string {
	if rest, ok := strings.CutPrefix(u, "webcal://"); ok {
		return "https://" + rest
	}
	return u
}

func (icalFeed) Sync(ctx context.Context, a *App, in integrationRow) error {
	var cur feedCursor
	json.Unmarshal([]byte(in.cursor), &cur) // "" is no cursor yet
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feedURL(string(in.credentials)), nil)
	if err != nil {
		return errors.New("the feed address isn't a URL")
	}
	req.Header.Set("User-Agent", "GoCasa")
	if cur.ETag != "" {
		req.Header.Set("If-None-Match", cur.ETag)
	}
	if cur.LastModified != "" {
		req.Header.Set("If-Modified-Since", cur.LastModified)
	}
	res, err := feedClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotModified:
		return nil
	case res.StatusCode != http.StatusOK:
		return fmt.Errorf("the feed answered %s", res.Status)
	}
	cal, err := ical.NewDecoder(http.MaxBytesReader(nil, res.Body, maxFeedBytes)).Decode()
	if err != nil {
		return fmt.Errorf("that isn't an iCal feed: %w", err)
	}
	next, _ := json.Marshal(feedCursor{ETag: res.Header.Get("ETag"), LastModified: res.Header.Get("Last-Modified")})
	evs, kept := parseFeed(cal, a.location(ctx))
	return a.storeFeed(ctx, in.id, evs, kept, string(next))
}

// storeFeed makes integration id's calendar match evs, and saves its next
// cursor, in one transaction: rows are upserted by UID, RECURRENCE-ID
// overrides rebuilt, and events gone from the feed cancelled; those with UIDs
// in kept stay as they were.
func (a *App) storeFeed(ctx context.Context, id int64, evs []feedEvent, kept []string, cursor string) error {
	var cal int64
	err := a.inTx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, "SELECT id FROM source_calendars WHERE integration_id = ?", id).Scan(&cal); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM events WHERE calendar_id = ? AND recurring_master_id IS NOT NULL", cal); err != nil {
			return err
		}
		now := a.Now().Unix()
		series := map[string]int64{} // UID to its master row
		seen := append([]string{}, kept...)
		written := 0
		write := func(key, master, original any, e feedEvent) error { // an override has no key
			status := "confirmed"
			if e.cancelled {
				status = "cancelled"
			}
			c := e.c
			var row int64
			err := tx.QueryRowContext(ctx, `INSERT INTO events (calendar_id, external_id, title, location, description, all_day, start_utc, end_utc, start_tzid, all_day_date, all_day_end_date,
					rrule, exdates_json, rdates_json, recurring_master_id, original_start_utc, status, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT (calendar_id, external_id) WHERE external_id IS NOT NULL DO UPDATE SET title = excluded.title, location = excluded.location,
					description = excluded.description, all_day = excluded.all_day, start_utc = excluded.start_utc, end_utc = excluded.end_utc,
					start_tzid = excluded.start_tzid, all_day_date = excluded.all_day_date, all_day_end_date = excluded.all_day_end_date,
					rrule = excluded.rrule, exdates_json = excluded.exdates_json, rdates_json = excluded.rdates_json,
					status = excluded.status, updated_at = excluded.updated_at
				RETURNING id`,
				cal, key, c.title, c.location, c.description, c.allDay, c.start, c.end, c.tzid, c.date, c.endDate,
				c.rrule, c.exdates, c.rdates, master, original, status, now).Scan(&row)
			if err != nil {
				return err
			}
			if c.rrule != nil {
				series[e.uid] = row
			}
			written++
			syncFault(written)
			return nil
		}
		for _, e := range evs { // Series first, so their overrides find them
			if e.original == 0 {
				seen = append(seen, e.uid)
				if err := write(e.uid, nil, nil, e); err != nil {
					return err
				}
			}
		}
		overridden := map[[2]int64]bool{}
		for _, e := range evs {
			if e.original == 0 {
				continue
			}
			m, ok := series[e.uid]
			if !ok { // an override of no Series here: an event of its own
				key := e.uid + "/" + strconv.FormatInt(e.original, 10)
				seen = append(seen, key)
				if err := write(key, nil, nil, e); err != nil {
					return err
				}
			} else if !overridden[[2]int64{m, e.original}] {
				overridden[[2]int64{m, e.original}] = true
				if err := write(nil, m, e.original, e); err != nil {
					return err
				}
			}
		}
		keys, _ := json.Marshal(seen)
		if _, err := tx.ExecContext(ctx, `UPDATE events SET status = 'cancelled', updated_at = ? WHERE calendar_id = ? AND recurring_master_id IS NULL
			AND status = 'confirmed' AND external_id NOT IN (SELECT value FROM json_each(?))`, now, cal, string(keys)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE integrations SET sync_cursor = ? WHERE id = ?", cursor, id)
		return err
	}, KindEvent) // shortcut: a 200 with the same feed still publishes (a 304 doesn't); compare a body hash in the cursor if feeds without ETags refetch pages too often
	return err
}

// parseFeed is cal's VEVENTs in their stored form, and the UIDs of those it
// can't read, whose last good copy is kept. home reads floating times.
func parseFeed(cal *ical.Calendar, home *time.Location) (evs []feedEvent, kept []string) {
	for _, ev := range cal.Events() {
		if e, ok := parseVEvent(ev, home); ok {
			evs = append(evs, e)
		} else if e.uid != "" {
			kept = append(kept, e.uid)
		}
	}
	return evs, kept
}

func parseVEvent(ev ical.Event, home *time.Location) (feedEvent, bool) {
	text := func(name string, limit int) string {
		s, _ := ev.Props.Text(name)
		r := []rune(strings.TrimSpace(s))
		return string(r[:min(len(r), limit)])
	}
	e := feedEvent{uid: text(ical.PropUID, 1000)}
	startProp := ev.Props.Get(ical.PropDateTimeStart)
	if e.uid == "" || startProp == nil {
		return e, false
	}
	start, zone, err := feedTime(startProp, home)
	if err != nil {
		return e, false
	}
	end := start
	if p := ev.Props.Get(ical.PropDateTimeEnd); p != nil {
		end, _, err = feedTime(p, home)
	} else if p := ev.Props.Get(ical.PropDuration); p != nil {
		var d time.Duration
		d, err = p.Duration()
		end = start.Add(d)
	}
	if err != nil {
		return e, false
	}
	status, _ := ev.Status()
	e.cancelled = status == ical.EventCancelled
	c := eventChange{title: text(ical.PropSummary, 200), location: text(ical.PropLocation, 200), description: text(ical.PropDescription, 5000),
		allDay: zone == nil, exdates: "[]", rdates: "[]"}
	if c.title == "" {
		c.title = "(No title)"
	}
	if c.allDay {
		d0, d1 := start.Format(dateLayout), end.Format(dateLayout)
		if d1 <= d0 { // no DTEND is one day (RFC 5545)
			d1 = start.AddDate(0, 0, 1).Format(dateLayout)
		}
		c.date, c.endDate = d0, d1
	} else {
		c.start, c.end, c.tzid = start.Unix(), max(end.Unix(), start.Unix()+1), zone.String() // the events CHECK needs end after start
	}
	if p := ev.Props.Get(ical.PropRecurrenceID); p != nil {
		t, z, err := feedTime(p, home)
		if err != nil {
			return e, false
		}
		e.original = t.Unix() // a date's is its UTC midnight, as expand keys it
		if z == nil && !c.allDay || z != nil && c.allDay {
			return e, false
		}
	} else {
		if p := ev.Props.Get(ical.PropRecurrenceRule); p != nil {
			if opt, err := rrule.StrToROption(p.Value); err == nil {
				if _, err := rrule.NewRRule(*opt); err == nil {
					c.rrule = p.Value
				}
			}
		}
		if c.rrule == nil && ev.Props.Get(ical.PropRecurrenceDates) != nil {
			c.rrule = "FREQ=DAILY;COUNT=1" // RDATEs alone: DTSTART once, plus them
		}
		if c.rrule != nil {
			ex, _ := json.Marshal(feedDates(ev.Props.Values(ical.PropExceptionDates), zone, home))
			rd, _ := json.Marshal(feedDates(ev.Props.Values(ical.PropRecurrenceDates), zone, home))
			c.exdates, c.rdates = string(ex), string(rd)
		}
	}
	e.c = c
	return e, true
}

// feedTime reads a DATE or DATE-TIME property: a date (zone nil, at UTC
// midnight), else the instant and its zone. A floating time, or one whose
// TZID Go doesn't know, is read in home.
// shortcut: a TZID defined only by the feed's VTIMEZONE (e.g. Outlook's "Pacific Standard Time") is read in home; map Windows zone names if feeds need it.
func feedTime(p *ical.Prop, home *time.Location) (time.Time, *time.Location, error) {
	v := p.Value
	if p.ValueType() == ical.ValueDate || len(v) == len("20060102") {
		t, err := time.Parse("20060102", v)
		return t, nil, err
	}
	if strings.HasSuffix(v, "Z") {
		t, err := time.Parse("20060102T150405Z", v)
		return t.Truncate(time.Minute), time.UTC, err // minutes, as expand keys Occurrences
	}
	zone := home
	if tzid := p.Params.Get(ical.PropTimezoneID); tzid != "" && tzid != "Local" {
		if z, err := time.LoadLocation(tzid); err == nil {
			zone = z
		}
	}
	t, err := time.Parse("20060102T150405", v)
	if err != nil {
		return t, nil, err
	}
	t, err = parseLocal(t.Format(localLayout), zone)
	return t, zone, err
}

// feedDates is EXDATE or RDATE values as a Series stores them: wall times in
// its zone, or dates when it is nil. PERIOD values are left out.
func feedDates(props []ical.Prop, zone, home *time.Location) []string {
	out := []string{}
	for _, p := range props {
		for _, v := range strings.Split(p.Value, ",") {
			one := p
			one.Value = v
			t, z, err := feedTime(&one, home)
			switch {
			case err != nil:
			case zone == nil:
				out = append(out, t.Format(dateLayout))
			case z != nil:
				out = append(out, t.In(zone).Format(localLayout))
			}
		}
	}
	return out
}
