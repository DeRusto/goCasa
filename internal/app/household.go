package app

import (
	"context"
	"database/sql"
	"net/http"
	"sync"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
)

// Household is the household_settings row and the home zone it names. It is
// the only reader of that table: a request loads it at most once (see
// withHousehold), and scheduler jobs, with no request, load it each time.
type Household struct {
	web.Household
	Zone           *time.Location // Timezone, else Config.Location
	Units          string         // "metric" or "imperial"
	WeatherLat     sql.NullFloat64
	WeatherLon     sql.NullFloat64 // NULL with WeatherLat: no weather
	FallbackFolder bool            // the Fallback folder is on
	Locks          web.Locks
}

// Today is midnight starting now's day in the Household's zone.
func (h Household) Today(now time.Time) time.Time { return dayAt(now.In(h.Zone), 0) }

// Clock is the time layout under the 12/24-hour setting.
func (h Household) Clock() string { return clockLayout(h.Clock24) }

// access is what this request's device may do: the Household's Locks, and
// whether an Admin has unlocked it.
func (a *App) access(ctx context.Context) (web.Access, error) {
	h, err := a.household(ctx)
	return web.Access{Locks: h.Locks, Unlocked: unlockedAdmin(ctx) != 0}, err
}

// setLocks writes the Household's Locks.
func (a *App) setLocks(ctx context.Context, l web.Locks) error {
	_, err := a.exec(ctx, web.KindHousehold, "UPDATE household_settings SET lock_calendar = ?, lock_chores = ?, lock_todos = ?",
		l.Calendar, l.Chores, l.Todos)
	return err
}

// loadHousehold reads household_settings. On error, Zone is still set (the
// server's), so pages that only need the zone can fall back.
func (a *App) loadHousehold(ctx context.Context) (Household, error) {
	a.householdReads.Add(1)
	h := Household{Zone: a.Config.Location}
	err := a.DB.QueryRowContext(ctx, `SELECT default_view, clock_24h, week_start, screensaver_idle_seconds, seconds_per_photo,
		night_mode, night_start, night_end, timezone, units, weather_lat, weather_lon, fallback_folder_enabled,
		lock_calendar, lock_chores, lock_todos FROM household_settings`).
		Scan(&h.DefaultView, &h.Clock24, &h.WeekStart, &h.IdleSeconds, &h.SecondsPerPhoto,
			&h.NightMode, &h.NightStart, &h.NightEnd, &h.Timezone, &h.Units, &h.WeatherLat, &h.WeatherLon, &h.FallbackFolder,
			&h.Locks.Calendar, &h.Locks.Chores, &h.Locks.Todos)
	if loc, lerr := time.LoadLocation(h.Timezone); err == nil && h.Timezone != "" && lerr == nil {
		h.Zone = loc
	}
	return h, err
}

type householdKey struct{}

// withHousehold gives the request a Household loaded on first use, so a
// request reads household_settings at most once and static files not at all.
func (a *App) withHousehold(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		load := sync.OnceValues(func() (Household, error) { return a.loadHousehold(ctx) })
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, householdKey{}, load)))
	})
}

// household is the request's Household, or a fresh read outside a request.
func (a *App) household(ctx context.Context) (Household, error) {
	if load, ok := ctx.Value(householdKey{}).(func() (Household, error)); ok {
		return load()
	}
	return a.loadHousehold(ctx)
}

// location is the Household's zone. An unreadable setting falls back to the
// server's rather than failing the page.
func (a *App) location(ctx context.Context) *time.Location {
	h, err := a.household(ctx)
	if err != nil {
		a.Log.Error("reading household timezone", "err", err)
	}
	return h.Zone
}

// clock is the Household's clock layout for pages that only mention a time in
// passing: an unreadable setting falls back to 24-hour rather than failing the page.
func (a *App) clock(ctx context.Context) string {
	h, err := a.household(ctx)
	if err != nil {
		a.Log.Error("reading household settings", "err", err)
	}
	return clockLayout(err != nil || h.Clock24)
}
