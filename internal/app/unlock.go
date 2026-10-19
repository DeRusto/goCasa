package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// unlockIdle is how long an unlock survives with no activity (spec §9).
const unlockIdle = 5 * time.Minute

const unlockCookie = "gocasa_unlock"

type adminKey struct{}

// unlockedAdmin is the id of the Admin whose PIN unlocked this request's
// device, or 0 when the device is locked.
func unlockedAdmin(ctx context.Context) int64 {
	id, _ := ctx.Value(adminKey{}).(int64)
	return id
}

// withUnlock reads the device's unlock cookie: "<admin id>.<last active unix>.<mac>".
// A valid one counts as activity and is re-issued with the current time, so
// the unlock lasts until unlockIdle passes with no request from that browser.
// The Member must still be an active Admin, so a demoted, archived or deleted
// Admin is locked out. Automatic requests aren't activity: page scripts mark
// their refetches with X-Background, and the change stream can't set headers.
func (a *App) withUnlock(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := a.checkUnlock(r); id != 0 {
			if !background(r) {
				a.setUnlock(w, id)
			}
			r = r.WithContext(context.WithValue(r.Context(), adminKey{}, id))
		}
		next.ServeHTTP(w, r)
	})
}

// background reports a request no one asked for: a page's live refresh, clock
// tick or photo poll, or the change stream.
func background(r *http.Request) bool {
	return r.Header.Get("X-Background") != "" || r.URL.Path == "/events/stream"
}

func (a *App) checkUnlock(r *http.Request) int64 {
	c, err := r.Cookie(unlockCookie)
	if err != nil {
		return 0
	}
	payload, mac, ok := strings.Cut(c.Value, "~")
	if !ok || !hmac.Equal([]byte(mac), []byte(a.unlockMAC(payload))) {
		return 0
	}
	idStr, atStr, _ := strings.Cut(payload, ".")
	id, err1 := strconv.ParseInt(idStr, 10, 64)
	at, err2 := strconv.ParseInt(atStr, 10, 64)
	if err1 != nil || err2 != nil {
		return 0
	}
	if idle := a.Now().Sub(time.Unix(at, 0)); idle < 0 || idle >= unlockIdle {
		return 0
	}
	var isAdmin bool
	if err := a.DB.QueryRowContext(r.Context(), "SELECT role = 'admin' AND NOT archived FROM members WHERE id = ?", id).Scan(&isAdmin); err != nil || !isAdmin {
		return 0
	}
	return id
}

func (a *App) unlockMAC(payload string) string {
	m := hmac.New(sha256.New, a.Config.SessionSecret)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (a *App) setUnlock(w http.ResponseWriter, id int64) {
	payload := fmt.Sprintf("%d.%d", id, a.Now().Unix())
	a.unlockCookieOut(w, payload+"~"+a.unlockMAC(payload), 0)
}

func (a *App) clearUnlock(w http.ResponseWriter) { a.unlockCookieOut(w, "", -1) }

func (a *App) unlockCookieOut(w http.ResponseWriter, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     unlockCookie,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge, // 0: a browser-session cookie, per spec §9
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   a.Config.BaseURL.Scheme == "https",
	})
}
