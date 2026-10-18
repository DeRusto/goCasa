package app

import (
	"cmp"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/teambition/rrule-go"
)

const (
	maxOccurrences = 1000 // per Series in one window
	horizonYears   = 100  // a Series expands no further than this past its start
)

// series is how a Series repeats, as stored on its master row.
type series struct {
	rule            string   // an RRULE value, e.g. "FREQ=WEEKLY;BYDAY=MO"
	exdates, rdates []string // wall times in the event's zone (localLayout), or dates if all-day
}

// oneOff is a One-off edit row: a changed Occurrence, or a cancelled one.
type oneOff struct {
	original  int64
	cancelled bool
	ev        calendarEvent
}

var untilUTC = regexp.MustCompile(`UNTIL=\d{8}T\d{6}Z`)

// floating is t's wall clock as a UTC time. Expanding there keeps 09:00 at 09:00
// across DST; each result is put back in the event's zone afterwards.
func floating(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.UTC)
}

// overlaps reports whether e touches [from, to); all-day events compare dates in from's zone.
func overlaps(e calendarEvent, from, to time.Time) bool {
	if e.allDay {
		return e.date < to.Format(dateLayout) && e.endDate > from.Format(dateLayout)
	}
	return e.start < to.Unix() && e.end > from.Unix()
}

// expand is the Occurrences of master's Series that touch [from, to), with its
// One-off edits merged in. zone is the event's own (unused when all-day). An
// Occurrence's original is its unedited start: the UTC instant, or for all-day
// the date's UTC midnight.
func expand(master calendarEvent, s series, zone *time.Location, oneOffs []oneOff, from, to time.Time) ([]calendarEvent, error) {
	layout := localLayout
	var dtstart, stop time.Time
	var dur time.Duration
	if master.allDay {
		layout = dateLayout
		d0, err0 := time.Parse(dateLayout, master.date)
		d1, err1 := time.Parse(dateLayout, master.endDate)
		if err0 != nil || err1 != nil {
			return nil, errors.Join(err0, err1)
		}
		dtstart, dur = d0, d1.Sub(d0)
		stop, _ = time.Parse(dateLayout, to.Format(dateLayout))
	} else {
		dtstart = floating(time.Unix(master.start, 0).In(zone))
		dur = time.Duration(master.end-master.start) * time.Second
		stop = floating(to.In(zone)).Add(24 * time.Hour) // zone offsets are under a day
	}
	opt, err := rrule.StrToROption(s.rule)
	if err != nil {
		return nil, err
	}
	opt.Dtstart = dtstart
	// COUNT and a UTC UNTIL apply once each Occurrence is in its zone: a skipped
	// gap time isn't counted (RFC 5545), and UNTIL compares real instants.
	count, untilAt := opt.Count, time.Time{}
	opt.Count = 0
	if untilUTC.MatchString(s.rule) && zone != nil {
		untilAt, opt.Until = opt.Until, time.Time{}
	}
	r, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil, err
	}
	exdates := map[time.Time]bool{}
	for _, d := range s.exdates {
		if t, err := time.Parse(layout, d); err == nil {
			exdates[t] = true
		}
	}

	// place is the Occurrence at floating time f, or false in a spring-forward gap (spec §7.1).
	place := func(f time.Time) (calendarEvent, bool) {
		o := master
		if master.allDay {
			o.date, o.endDate, o.original = f.Format(dateLayout), f.Add(dur).Format(dateLayout), f.Unix()
			return o, true
		}
		t, err := parseLocal(f.Format(localLayout), zone)
		if err != nil || t.Format(localLayout) != f.Format(localLayout) {
			return o, false
		}
		o.start, o.end, o.original = t.Unix(), t.Add(dur).Unix(), t.Unix()
		return o, true
	}
	var out []calendarEvent
	seen := map[int64]bool{} // originals already edited or added
	for _, o := range oneOffs {
		seen[o.original] = true
		if !o.cancelled && overlaps(o.ev, from, to) {
			e := o.ev
			e.id, e.original = master.id, o.original
			out = append(out, e)
		}
	}
	add := func(f time.Time) {
		if o, ok := place(f); ok && !exdates[f] && !seen[o.original] && overlaps(o, from, to) {
			seen[o.original] = true
			out = append(out, o)
		}
	}
	// shortcut: iterates from DTSTART on every render (~36k steps for a century-old daily Series); start nearer the window if renders get slow.
	horizon := dtstart.AddDate(horizonYears, 0, 0)
	next, n := r.Iterator(), 0
	for f, ok := next(); ok && f.Before(stop) && f.Before(horizon) && len(out) < maxOccurrences; f, ok = next() {
		o, ok := place(f)
		if !ok {
			continue
		}
		if n++; count > 0 && n > count || !untilAt.IsZero() && o.start > untilAt.Unix() {
			break
		}
		add(f) // an EXDATE still uses up its COUNT slot
	}
	for _, d := range s.rdates {
		if t, err := time.Parse(layout, d); err == nil {
			add(t)
		}
	}
	key := func(e calendarEvent) int64 {
		if e.allDay {
			d, _ := time.Parse(dateLayout, e.date)
			return d.Unix()
		}
		return e.start
	}
	slices.SortStableFunc(out, func(x, y calendarEvent) int { return cmp.Compare(key(x), key(y)) })
	return out[:min(len(out), maxOccurrences)], nil
}
