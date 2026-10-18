package app

import (
	"errors"
	"regexp"
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
	if untilUTC.MatchString(s.rule) && zone != nil {
		opt.Until = floating(opt.Until.In(zone))
	}
	r, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil, err
	}
	var set rrule.Set
	set.RRule(r)
	for _, d := range s.exdates {
		if t, err := time.Parse(layout, d); err == nil {
			set.ExDate(t)
		}
	}
	for _, d := range s.rdates {
		if t, err := time.Parse(layout, d); err == nil {
			set.RDate(t)
		}
	}

	var out []calendarEvent
	edited := map[int64]bool{}
	for _, o := range oneOffs {
		edited[o.original] = true
		if !o.cancelled && overlaps(o.ev, from, to) {
			e := o.ev
			e.id, e.original = master.id, o.original
			out = append(out, e)
		}
	}
	// shortcut: iterates from DTSTART on every render (~36k steps for a century-old daily Series); start nearer the window if renders get slow.
	horizon := dtstart.AddDate(horizonYears, 0, 0)
	next := set.Iterator()
	for f, ok := next(); ok && f.Before(stop) && f.Before(horizon) && len(out) < maxOccurrences; f, ok = next() {
		o := master
		if master.allDay {
			o.date, o.endDate, o.original = f.Format(dateLayout), f.Add(dur).Format(dateLayout), f.Unix()
		} else {
			t, err := parseLocal(f.Format(localLayout), zone)
			if err != nil || t.Format(localLayout) != f.Format(localLayout) {
				continue // in a spring-forward gap: skipped (spec §7.1)
			}
			o.start, o.end, o.original = t.Unix(), t.Add(dur).Unix(), t.Unix()
		}
		if !edited[o.original] && overlaps(o, from, to) {
			out = append(out, o)
		}
	}
	return out, nil
}
