package app

import (
	"cmp"
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/DeRusto/goCasa/internal/web"
)

// defaultViews are the pages a Household's default_view can name.
var defaultViews = map[string]string{"today": "/today", "calendar": "/calendar", "todos": "/todos", "chores": "/chores", "lists": "/lists"}

const (
	defaultViewCookie = "default_view"
	themeCookie       = "theme"
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
	return d
}

// household is the household settings the pages follow.
func (a *App) household(ctx context.Context) (web.Household, error) {
	var h web.Household
	err := a.DB.QueryRowContext(ctx, "SELECT default_view, clock_24h, week_start FROM household_settings").Scan(&h.DefaultView, &h.Clock24, &h.WeekStart)
	return h, err
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

// saveHousehold sets the household Default view, clock and week start; it sits behind requireUnlock.
func (a *App) saveHousehold(w http.ResponseWriter, r *http.Request) {
	view := r.PostFormValue("default_view")
	start, err := strconv.Atoi(r.PostFormValue("week_start"))
	if _, ok := defaultViews[view]; !ok || err != nil || start < 0 || start > 6 {
		a.settingsPage(w, r, http.StatusBadRequest, "Pick a default view and the day weeks start on.")
		return
	}
	if _, err := a.DB.ExecContext(r.Context(), "UPDATE household_settings SET default_view = ?, clock_24h = ?, week_start = ?",
		view, r.PostForm.Has("clock_24h"), start); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
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
