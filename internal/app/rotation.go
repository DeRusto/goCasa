package app

import (
	"context"
	"database/sql"
	"errors"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
)

// The screensaver's photo rotation (ADR 0002): one no-repeat shuffle shared
// by every Wall display, dealt in steps of one photo or a Photo pair. The
// screensaver page, /screensaver/photo and the prefetcher use only
// screensaverStep and upcoming.

// photoDeck is the shuffle's state.
type photoDeck struct {
	mu      sync.Mutex
	steps   [][]int64 // photos_cache ids, one round
	pos     int       // steps[pos-1] is showing
	shownAt time.Time
	rnd     *rand.Rand
}

// photoCandidate is a photo the rotation may show.
type photoCandidate struct {
	id               int64
	cached, portrait bool
}

// deal is a new round: each candidate once, cached ones first, each part
// shuffled with rnd. A portrait waits for the next portrait to pair with,
// within its part, so a round never skips or repeats one; an odd one out
// shows alone.
func deal(cands []photoCandidate, rnd *rand.Rand) [][]int64 {
	c := slices.Clone(cands)
	rnd.Shuffle(len(c), func(i, j int) { c[i], c[j] = c[j], c[i] })
	slices.SortStableFunc(c, func(x, y photoCandidate) int {
		if x.cached == y.cached {
			return 0
		}
		if x.cached {
			return -1
		}
		return 1
	})
	var steps [][]int64
	waiting := -1 // the step holding an unpaired portrait
	for i, p := range c {
		if i > 0 && c[i-1].cached && !p.cached {
			waiting = -1 // a cached portrait never waits on an uncached one
		}
		switch {
		case !p.portrait:
			steps = append(steps, []int64{p.id})
		case waiting >= 0:
			steps[waiting] = append(steps[waiting], p.id)
			waiting = -1
		default:
			waiting = len(steps)
			steps = append(steps, []int64{p.id})
		}
	}
	return steps
}

// advance moves past pos to the next step with a photo still present, dealing
// a new round once when this one runs out; a nil step for none.
func advance(steps [][]int64, pos int, present map[int64]bool, redeal func() [][]int64) ([][]int64, int, []int64) {
	for range 2 {
		for ; pos < len(steps); pos++ {
			if st := presentOf(steps[pos], present); len(st) > 0 {
				return steps, pos + 1, st
			}
		}
		steps, pos = redeal(), 0
	}
	return steps, len(steps), nil
}

// presentOf is step without the photos gone since the shuffle.
func presentOf(step []int64, present map[int64]bool) []int64 {
	var st []int64
	for _, id := range step {
		if present[id] {
			st = append(st, id)
		}
	}
	return st
}

// screensaverStep is what a Wall display (portrait or not) shows now and the
// whole seconds it still shows: no photos for none. The step moves on once it
// has shown for the Household's seconds per photo. A portrait screen shows a
// Photo pair one photo after the other, half the time each, so it keeps step
// with landscape screens.
func (a *App) screensaverStep(ctx context.Context, now time.Time, hh Household, portrait bool) ([]web.Photo, int, error) {
	every := time.Duration(hh.SecondsPerPhoto) * time.Second
	d := &a.photos
	d.mu.Lock()
	cands, err := a.screensaverPhotos(ctx)
	if err != nil {
		d.mu.Unlock()
		return nil, 0, err
	}
	present := map[int64]bool{}
	for _, c := range cands {
		present[c.id] = true
	}
	var step []int64
	if d.pos > 0 && now.Sub(d.shownAt) < every {
		step = presentOf(d.steps[d.pos-1], present)
	}
	if len(step) == 0 {
		if d.rnd == nil {
			d.rnd = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
		}
		d.steps, d.pos, step = advance(d.steps, d.pos, present, func() [][]int64 { return deal(cands, d.rnd) })
		d.shownAt = now
	}
	left := every - now.Sub(d.shownAt)
	d.mu.Unlock()
	if len(step) == 0 {
		return nil, 0, nil
	}
	if portrait && len(step) == 2 {
		if half := every / 2; left > half {
			step, left = step[:1], left-half
		} else {
			step = step[1:]
		}
	}
	photos := make([]web.Photo, len(step))
	for i, id := range step {
		photos[i].Src = "/screensaver/photos/" + strconv.FormatInt(id, 10)
		if hh.PhotoCaptions {
			if photos[i].Caption, err = a.photoCaption(ctx, id); err != nil {
				return nil, 0, err
			}
		}
	}
	return photos, int(left.Seconds() + .999), nil
}

// upcoming is the next n photos the rotation will show after the current
// step's first, which a portrait screen follows with its pair's second.
func (a *App) upcoming(n int) []int64 {
	d := &a.photos
	d.mu.Lock()
	defer d.mu.Unlock()
	var next []int64
	if d.pos > 0 {
		next = slices.Clone(d.steps[d.pos-1][1:])
	}
	for _, st := range d.steps[d.pos:] {
		if len(next) >= n {
			break
		}
		next = append(next, st...)
	}
	return next[:min(n, len(next))]
}

// screensaverPhotos is the photos the screensaver rotates through, by id:
// Immich's, else the Fallback folder's when it is on. Immich has nothing to
// show when not connected, its list is empty, or it is failing with none of
// its photos cached. Never both.
func (a *App) screensaverPhotos(ctx context.Context) ([]photoCandidate, error) {
	im, err := a.photoList(ctx, photoSourceImmich)
	if err != nil {
		return nil, err
	}
	if len(im) > 0 {
		var failing bool
		if err := a.DB.QueryRowContext(ctx, "SELECT "+failingSQL("integrations")+" FROM integrations WHERE type = ?", immichType).Scan(&failing); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if failing { // only cached bytes can show, so rotate through just those
			im = slices.DeleteFunc(im, func(c photoCandidate) bool { return !c.cached })
		}
		if len(im) > 0 {
			return im, nil
		}
	}
	if hh, err := a.household(ctx); err != nil || !hh.FallbackFolder {
		return nil, err
	}
	folder, err := a.photoList(ctx, photoSourceFolder)
	for i := range folder {
		folder[i].cached = true // on disk already
	}
	return folder, err
}

// photoList is source's list, in id order, each with whether its bytes are
// cached and whether it is portrait (unknown sizes show alone, as landscape).
func (a *App) photoList(ctx context.Context, source string) ([]photoCandidate, error) {
	rows, err := a.DB.QueryContext(ctx, "SELECT id, cache_path IS NOT NULL, height > width FROM photos_cache WHERE source = ? ORDER BY id", source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cs []photoCandidate
	for rows.Next() {
		var c photoCandidate
		if err := rows.Scan(&c.id, &c.cached, &c.portrait); err != nil {
			return nil, err
		}
		cs = append(cs, c)
	}
	return cs, rows.Err()
}

// photoCaption is when photo id was taken and where, if known, e.g.
// "June 2019 · Lisbon, Portugal"; "" when neither is.
func (a *App) photoCaption(ctx context.Context, id int64) (string, error) {
	var taken sql.NullInt64
	var place string
	err := a.DB.QueryRowContext(ctx, "SELECT taken_at, place FROM photos_cache WHERE id = ?", id).Scan(&taken, &place)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil // gone since the step was dealt
	}
	if err != nil {
		return "", err
	}
	var parts []string
	if taken.Valid && taken.Int64 > 0 {
		// UTC: taken_at is the local time it was taken, stored as UTC.
		parts = append(parts, time.Unix(taken.Int64, 0).UTC().Format("January 2006"))
	}
	if place != "" {
		parts = append(parts, place)
	}
	return strings.Join(parts, " · "), nil
}
