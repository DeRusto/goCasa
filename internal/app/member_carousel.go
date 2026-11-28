package app

import (
	"net/http"
	"net/url"
	"slices"

	"github.com/DeRusto/goCasa/internal/web"
)

// carouselQuery carries the open tile and skip order through a tick or the PIN
// pad. The group comes from the handler, and the destination is always local.
func carouselQuery(r *http.Request, group string) string {
	q := r.URL.Query()
	if q.Get("carousel") != group {
		return ""
	}
	return "?" + url.Values{"carousel": {group}, "later": q["later"]}.Encode()
}

const routineCelebrationCookie = "gocasa_routine_done"

// A foreground Member page consumes the cue after the mutation's redirect.
// Background refreshes and reloads should never start a celebration.
func (a *App) routineCelebration(w http.ResponseWriter, path, routine string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     routineCelebrationCookie,
		Value:    routine,
		Path:     path,
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.Config.BaseURL.Scheme == "https",
	})
}

func (a *App) takeRoutineCelebration(w http.ResponseWriter, r *http.Request, groups []web.ChoreGroup) string {
	c, err := r.Cookie(routineCelebrationCookie)
	if err != nil || background(r) {
		return ""
	}
	a.routineCelebration(w, r.URL.Path, "", -1)
	if !slices.Contains(web.Routines, c.Value) {
		return ""
	}
	for _, g := range groups {
		if g.Routine == c.Value {
			if done, total := g.Progress(); total > 0 && done == total && !g.Pending() {
				return g.Routine
			}
			break
		}
	}
	return ""
}
