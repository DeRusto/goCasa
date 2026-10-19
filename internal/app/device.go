package app

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
)

// defaultViews are the pages a Household's default_view can name.
var defaultViews = map[string]string{"today": "/today", "calendar": "/calendar", "todos": "/todos", "chores": "/chores", "lists": "/lists"}

const (
	defaultViewCookie = "default_view"
	themeCookie       = "theme"
	wallDisplayCookie = "wall_display"
	motionWakeCookie  = "motion_wake"
	deviceCookieAge   = 10 * 365 * 24 * 60 * 60 // a device keeps its choices until cleared
)

// calendarView is a calendar view and Member filter as a Default view: no
// date, so it always opens on today.
func calendarView(view string, users []int64) string {
	q := url.Values{"view": {view}}
	if len(users) > 0 {
		ss := make([]string, len(users))
		for i, id := range users {
			ss[i] = strconv.FormatInt(id, 10)
		}
		q.Set("users", strings.Join(ss, ","))
	}
	return "/calendar?" + q.Encode()
}

// deviceView is v as a Default view, or false when it names none. v comes from
// the browser, so only these local paths are let through.
func deviceView(v string) (string, bool) {
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "", false
	}
	switch u.Path {
	case "/today", "/todos", "/chores", "/lists":
		return u.Path, true
	case "/calendar":
		view := u.Query().Get("view")
		if view != "week" && view != "day" && view != "agenda" {
			view = "month"
		}
		return calendarView(view, parseIDs(u.Query()["users"])), true
	}
	return "", false
}

// cookieView is this device's Default view, if it set one.
func cookieView(r *http.Request) (string, bool) {
	c, err := r.Cookie(defaultViewCookie)
	if err != nil {
		return "", false
	}
	v, err := url.QueryUnescape(c.Value)
	if err != nil {
		return "", false
	}
	return deviceView(v)
}

// deviceOf is this browser's choices from its cookies.
func deviceOf(r *http.Request) web.Device {
	d := web.Device{Theme: "auto"}
	if c, err := r.Cookie(themeCookie); err == nil && (c.Value == "light" || c.Value == "dark") {
		d.Theme = c.Value
	}
	d.DefaultView, _ = cookieView(r)
	_, err := r.Cookie(wallDisplayCookie)
	d.WallDisplay = err == nil
	_, err = r.Cookie(motionWakeCookie)
	d.MotionWake = d.WallDisplay && err == nil
	return d
}

// household is the household settings the pages follow.
func (a *App) household(ctx context.Context) (web.Household, error) {
	var h web.Household
	err := a.DB.QueryRowContext(ctx, "SELECT default_view, clock_24h, week_start, screensaver_idle_seconds, seconds_per_photo, night_mode, night_start, night_end, timezone FROM household_settings").
		Scan(&h.DefaultView, &h.Clock24, &h.WeekStart, &h.IdleSeconds, &h.SecondsPerPhoto, &h.NightMode, &h.NightStart, &h.NightEnd, &h.Timezone)
	return h, err
}

// location is the Household's timezone, else the server's (TZ). An unreadable
// setting falls back too rather than failing the page.
func (a *App) location(ctx context.Context) *time.Location {
	var tz string
	if err := a.DB.QueryRowContext(ctx, "SELECT timezone FROM household_settings").Scan(&tz); err != nil {
		a.Log.Error("reading household timezone", "err", err)
	}
	if loc, err := time.LoadLocation(tz); tz != "" && err == nil {
		return loc
	}
	return a.Config.Location
}

// clockLayout is how times show under the Household's 12/24-hour setting.
func clockLayout(h24 bool) string {
	if h24 {
		return "15:04"
	}
	return "3:04 PM"
}

// clock is clockLayout for pages that only mention a time in passing: an
// unreadable setting falls back to 24-hour rather than failing the page.
func (a *App) clock(ctx context.Context) string {
	h, err := a.household(ctx)
	if err != nil {
		a.Log.Error("reading household settings", "err", err)
	}
	return clockLayout(err != nil || h.Clock24)
}

// saveHousehold sets the household Default view, clock, week start and
// screensaver idle time, seconds per photo and Night mode (each kept when not
// sent); it sits behind requireUnlock.
func (a *App) saveHousehold(w http.ResponseWriter, r *http.Request) {
	view := r.PostFormValue("default_view")
	start, err := strconv.Atoi(r.PostFormValue("week_start"))
	idle, ok := optInt(r, "screensaver_idle_seconds", 10)
	if !ok {
		a.settingsPage(w, r, http.StatusBadRequest, "The screensaver idle time is at least 10 seconds.")
		return
	}
	perPhoto, ok := optInt(r, "seconds_per_photo", 5)
	if !ok {
		a.settingsPage(w, r, http.StatusBadRequest, "Each photo shows for at least 5 seconds.")
		return
	}
	var night *string
	var nightStart, nightEnd *int
	if v := r.PostFormValue("night_mode"); v != "" {
		s, err1 := strconv.Atoi(r.PostFormValue("night_start"))
		e, err2 := strconv.Atoi(r.PostFormValue("night_end"))
		if (v != "off" && v != "black" && v != "dim_clock") || err1 != nil || err2 != nil || s < 0 || s > 23 || e < 0 || e > 23 {
			a.settingsPage(w, r, http.StatusBadRequest, "Pick a Night mode and its start and end hours (0 to 23).")
			return
		}
		night, nightStart, nightEnd = &v, &s, &e
	}
	var tz *string
	if r.PostForm.Has("timezone") {
		v := strings.TrimSpace(r.PostFormValue("timezone"))
		if _, err := time.LoadLocation(v); err != nil || strings.EqualFold(v, "local") {
			a.settingsPage(w, r, http.StatusBadRequest, "Pick a timezone by its IANA name, like Europe/London, or leave it blank for the server's.")
			return
		}
		tz = &v
	}
	if _, ok := defaultViews[view]; !ok || err != nil || start < 0 || start > 6 {
		a.settingsPage(w, r, http.StatusBadRequest, "Pick a default view and the day weeks start on.")
		return
	}
	if _, err := a.DB.ExecContext(r.Context(), `UPDATE household_settings SET default_view = ?, clock_24h = ?, week_start = ?,
		screensaver_idle_seconds = coalesce(?, screensaver_idle_seconds), seconds_per_photo = coalesce(?, seconds_per_photo),
		night_mode = coalesce(?, night_mode), night_start = coalesce(?, night_start), night_end = coalesce(?, night_end),
		timezone = coalesce(?, timezone)`,
		view, r.PostForm.Has("clock_24h"), start, idle, perPhoto, night, nightStart, nightEnd, tz); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// optInt is form field name as a number of at least min, nil when not sent;
// ok is false when it was sent but isn't one.
func optInt(r *http.Request, name string, min int) (n *int, ok bool) {
	v := r.PostFormValue(name)
	if v == "" {
		return nil, true
	}
	i, err := strconv.Atoi(v)
	if err != nil || i < min {
		return nil, false
	}
	return &i, true
}

// getRoot sends the browser to this device's Default view, else the Household's.
func (a *App) getRoot(w http.ResponseWriter, r *http.Request) {
	if v, ok := cookieView(r); ok {
		http.Redirect(w, r, v, http.StatusSeeOther)
		return
	}
	h, err := a.household(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, cmp.Or(defaultViews[h.DefaultView], "/today"), http.StatusSeeOther)
}

// setTheme saves this device's theme in its browser; Auto clears it.
func (a *App) setTheme(w http.ResponseWriter, r *http.Request) {
	c := &http.Cookie{Name: themeCookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1}
	if t := r.PostFormValue("theme"); t == "light" || t == "dark" {
		c.Value, c.MaxAge = t, deviceCookieAge
	}
	http.SetCookie(w, c)
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// setWallDisplay switches this browser into or out of a Wall display; it sits
// behind requireUnlock (Admin PIN needed).
func (a *App) setWallDisplay(w http.ResponseWriter, r *http.Request) {
	c := &http.Cookie{Name: wallDisplayCookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1}
	if r.PostFormValue("wall_display") != "" {
		c.Value, c.MaxAge = "on", deviceCookieAge
	}
	http.SetCookie(w, c)
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// setMotionWake switches this Wall display's camera motion-wake on or off; it
// sits behind requireUnlock (Admin PIN needed).
func (a *App) setMotionWake(w http.ResponseWriter, r *http.Request) {
	c := &http.Cookie{Name: motionWakeCookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1}
	if r.PostFormValue("motion_wake") != "" {
		c.Value, c.MaxAge = "on", deviceCookieAge
	}
	http.SetCookie(w, c)
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// getScreensaver is what an idle Wall display shows: the clock and today's
// agenda over the current Immich photo, else on dark, or Night mode in its
// hours. Entering it ends any Admin unlock; a touch goes to "/", the Default
// view at today.
func (a *App) getScreensaver(w http.ResponseWriter, r *http.Request) {
	a.clearUnlock(w) // after withUnlock's refresh, so this later Set-Cookie wins
	now := a.Now().In(a.location(r.Context()))
	today := dayAt(now, 0)
	hh, err := a.household(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	clock := clockLayout(hh.Clock24)
	if mode := nightModeAt(hh, now.Hour()); mode != "off" {
		a.render(w, r, http.StatusOK, web.Night(mode, web.TodayPage{Now: now, Clock: now.Format(clock), Clock24: hh.Clock24}))
		return
	}
	evs, err := a.eventsIn(r.Context(), today, dayAt(today, 1), nil)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	src, left, err := a.photoNow(r.Context(), now, hh)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, web.Screensaver(web.TodayPage{
		Now: now, Clock: now.Format(clock), Clock24: hh.Clock24,
		Days: [2]web.CalendarDay{calendarDay(evs, today, today, now.Unix(), clock)},
	}, src, left))
}

// photoNow is the screensaver photo's address ("" for none) and the seconds it has left.
func (a *App) photoNow(ctx context.Context, now time.Time, hh web.Household) (string, int, error) {
	photo, left, err := a.currentPhoto(ctx, now, time.Duration(hh.SecondsPerPhoto)*time.Second)
	if err != nil || photo == 0 {
		return "", int(left.Seconds() + .999), err
	}
	return "/screensaver/photos/" + strconv.FormatInt(photo, 10), int(left.Seconds() + .999), nil
}

// getScreensaverPhoto tells an open screensaver its current photo, so it
// swaps the image in place and its camera (motion-wake) stays up.
func (a *App) getScreensaverPhoto(w http.ResponseWriter, r *http.Request) {
	hh, err := a.household(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	src, left, err := a.photoNow(r.Context(), a.Now(), hh)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(struct {
		Src     string `json:"src"`
		Seconds int    `json:"seconds"`
	}{src, left})
}

// nightModeAt is the Night mode in force at hour (0-23): the household's
// between night_start and night_end (wrapping past midnight), else "off".
// Equal start and end means no night.
func nightModeAt(h web.Household, hour int) string {
	s, e := h.NightStart, h.NightEnd
	if (s < e && hour >= s && hour < e) || (s > e && (hour >= s || hour < e)) {
		return h.NightMode
	}
	return "off"
}

// setDefaultView saves this device's Default view in its browser; a blank one
// clears it. No Lock: it changes only this device.
func (a *App) setDefaultView(w http.ResponseWriter, r *http.Request) {
	c := &http.Cookie{Name: defaultViewCookie, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode}
	back := "/settings"
	if raw := r.PostFormValue("default_view"); raw == "" {
		c.MaxAge = -1
	} else if v, ok := deviceView(raw); ok {
		c.Value, c.MaxAge, back = url.QueryEscape(v), deviceCookieAge, v
	} else {
		http.Error(w, "not a view", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, c)
	http.Redirect(w, r, back, http.StatusSeeOther)
}
