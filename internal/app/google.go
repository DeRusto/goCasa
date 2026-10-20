package app

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
	"github.com/a-h/templ"
)

// Connecting Google accounts (spec §7.2) with the Household's own OAuth client.
// Syncing them is googleSync.

const (
	googleType = "google_calendar"
	// calendar.events doesn't cover CalendarList.list, hence calendarlist.readonly.
	googleScopes       = "openid email https://www.googleapis.com/auth/calendar.events https://www.googleapis.com/auth/calendar.calendarlist.readonly"
	googleStateCookie  = "gocasa_google_state"
	maxGoogleCalendars = 100
	defaultGoogleColor = "#3b82f6"
)

var googleClient = &http.Client{Timeout: 30 * time.Second}

// googleToken is a Google account's sealed credentials.
type googleToken struct {
	RefreshToken string `json:"refresh_token"`
}

// googleGrant is what a finished consent gives: tokens and who signed in.
type googleGrant struct {
	refresh, access string
	sub, email      string
}

type googleCalendar struct {
	ID              string `json:"id"`
	Summary         string `json:"summary"`
	SummaryOverride string `json:"summaryOverride"`
	BackgroundColor string `json:"backgroundColor"`
	AccessRole      string `json:"accessRole"`
}

// connectGoogle sends the browser to Google's consent page, to add a Google
// account or, with account set, to reconnect that one.
func (a *App) connectGoogle(w http.ResponseWriter, r *http.Request) {
	if a.Config.GoogleClientID == "" {
		a.settingsPage(w, r, http.StatusBadRequest, "Google isn't set up: set GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET (see the README).")
		return
	}
	q := url.Values{
		"client_id":     {a.Config.GoogleClientID},
		"redirect_uri":  {a.Config.GoogleRedirectURI},
		"response_type": {"code"},
		"scope":         {googleScopes},
		"access_type":   {"offline"},
		"prompt":        {"consent"}, // else a second consent brings no refresh token
	}
	account := r.PostFormValue("account")
	if account != "" {
		var email string
		if err := a.DB.QueryRowContext(r.Context(), "SELECT name FROM integrations WHERE id = ? AND type = ?", account, googleType).Scan(&email); errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		} else if err != nil {
			a.serverError(w, r, err)
			return
		}
		q.Set("login_hint", email)
	}
	state := rand.Text()
	q.Set("state", state)
	http.SetCookie(w, &http.Cookie{
		Name:     googleStateCookie,
		Value:    state + "." + account,
		Path:     "/auth/google/callback",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode, // Google's redirect back is a top-level GET
		Secure:   a.Config.BaseURL.Scheme == "https",
	})
	http.Redirect(w, r, a.Upstreams.GoogleAuth+"/o/oauth2/v2/auth?"+q.Encode(), http.StatusSeeOther)
}

// googleCallback finishes a consent: it stores the account's token and lists
// its calendars. A reconnect keeps the account's calendars, Member defaults
// and cursors, and refuses a different login.
func (a *App) googleCallback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(googleStateCookie)
	http.SetCookie(w, &http.Cookie{Name: googleStateCookie, Path: "/auth/google/callback", MaxAge: -1})
	var state, account string
	if err == nil {
		state, account, _ = strings.Cut(c.Value, ".")
	}
	if state == "" || subtle.ConstantTimeCompare([]byte(state), []byte(r.FormValue("state"))) != 1 {
		a.settingsPage(w, r, http.StatusBadRequest, "That Google sign-in had expired or didn't start here. Try connecting again.")
		return
	}
	if e := r.FormValue("error"); e != "" {
		a.settingsPage(w, r, http.StatusBadRequest, "Google didn't connect the account ("+e+").")
		return
	}
	g, err := a.googleExchange(r.Context(), r.FormValue("code"))
	if err != nil {
		a.Log.Warn("google connect failed", "err", err)
		a.settingsPage(w, r, http.StatusBadGateway, "Couldn't finish connecting to Google: "+err.Error())
		return
	}
	// Only Google's own errors are shown; anything of GoCasa's is a server error.
	var msg string
	cals, gerr := a.googleCalendars(r.Context(), g.access)
	if gerr == nil {
		msg, err = a.storeGoogleAccount(r.Context(), account, g, cals)
	}
	if gerr != nil || msg != "" || err != nil {
		// Don't leave a grant behind that GoCasa won't use. Revoking ends the
		// login's whole grant, so not for a login that's connected here.
		// Its own context: a browser closed mid-connect still gets cleaned up after.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
		defer cancel()
		var known bool
		qerr := a.DB.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM integrations WHERE type = ? AND account_sub = ?)", googleType, g.sub).Scan(&known)
		if qerr == nil && !known {
			if rerr := a.revokeGoogle(ctx, g.refresh); rerr != nil {
				a.Log.Warn("revoking an unused google token failed", "err", rerr)
			}
		}
	}
	switch {
	case gerr != nil:
		a.Log.Warn("google connect failed", "err", gerr)
		a.settingsPage(w, r, http.StatusBadGateway, "Couldn't finish connecting to Google: "+gerr.Error())
	case err != nil:
		a.serverError(w, r, err)
	case msg != "":
		a.settingsPage(w, r, http.StatusConflict, msg)
	default:
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
	}
}

// storeGoogleAccount saves g as Google account account (an integration id, or
// "" to add one) with its calendars cals. msg refuses the login, for the person.
func (a *App) storeGoogleAccount(ctx context.Context, account string, g googleGrant, cals []googleCalendar) (msg string, err error) {
	tok, _ := json.Marshal(googleToken{RefreshToken: g.refresh})
	// A reconnect ends Reconnect needed and any backoff: sync straight away.
	return a.integrations.Connect(ctx, connection{typ: googleType, name: g.email, secret: string(tok), interval: googleSyncInterval, sub: g.sub,
		reconnect: func(tx *sql.Tx) (id int64, msg string, err error) {
			if account == "" {
				// Connecting a login that's already here reconnects it.
				err = tx.QueryRowContext(ctx, "SELECT id FROM integrations WHERE type = ? AND account_sub = ?", googleType, g.sub).Scan(&id)
				if errors.Is(err, sql.ErrNoRows) {
					return 0, "", nil
				}
				return id, "", err
			}
			var sub string
			err = tx.QueryRowContext(ctx, "SELECT id, account_sub FROM integrations WHERE id = ? AND type = ?", account, googleType).Scan(&id, &sub)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				return 0, "That Google account was disconnected meanwhile. Connect it again as a new account.", nil
			case err != nil:
				return 0, "", err
			case sub != g.sub:
				return 0, "That's a different Google account. Add it as a new connection instead.", nil
			}
			return id, "", nil
		},
		then: func(tx *sql.Tx, id int64) error {
			for _, c := range cals {
				name := []rune(strings.TrimSpace(c.SummaryOverride))
				if len(name) == 0 {
					name = []rune(strings.TrimSpace(c.Summary))
				}
				if len(name) == 0 {
					name = []rune(c.ID)
				}
				color := strings.ToLower(c.BackgroundColor)
				if !colorRE.MatchString(color) {
					color = defaultGoogleColor
				}
				readOnly := c.AccessRole != "owner" && c.AccessRole != "writer"
				// New calendars start hidden until an Admin picks them; known ones keep their Admin's choices but take Google's name, colour and access.
				if _, err := tx.ExecContext(ctx, `INSERT INTO source_calendars (integration_id, external_id, name, color, enabled, read_only) VALUES (?, ?, ?, ?, 0, ?)
				ON CONFLICT (integration_id, external_id) WHERE external_id IS NOT NULL DO UPDATE SET name = excluded.name, color = excluded.color, read_only = excluded.read_only`,
					id, c.ID, string(name[:min(len(name), 50)]), color, readOnly); err != nil {
					return err
				}
			}
			return nil
		}})
}

// googleExchange trades a consent's code for tokens. The ID token comes
// straight from Google's token endpoint over TLS, so its claims are trusted
// without checking its signature (OpenID Connect Core §3.1.3.7).
func (a *App) googleExchange(ctx context.Context, code string) (googleGrant, error) {
	var body struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		Error        string `json:"error"`
	}
	status, err := a.googlePost(ctx, "/token", url.Values{
		"code":          {code},
		"client_id":     {a.Config.GoogleClientID},
		"client_secret": {a.Config.GoogleClientSecret},
		"redirect_uri":  {a.Config.GoogleRedirectURI},
		"grant_type":    {"authorization_code"},
	}, &body)
	switch {
	case err != nil:
		return googleGrant{}, err
	case status != http.StatusOK:
		return googleGrant{}, fmt.Errorf("Google refused the sign-in (%d %s)", status, body.Error)
	case body.RefreshToken == "" || body.AccessToken == "":
		return googleGrant{}, errors.New("Google sent no refresh token")
	}
	var claims struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Aud   string `json:"aud"`
		Iss   string `json:"iss"`
		Exp   int64  `json:"exp"`
	}
	parts := strings.Split(body.IDToken, ".")
	if len(parts) != 3 {
		return googleGrant{}, errors.New("Google sent no ID token")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(raw, &claims) != nil || claims.Sub == "" || claims.Aud != a.Config.GoogleClientID ||
		(claims.Iss != "https://accounts.google.com" && claims.Iss != "accounts.google.com") || claims.Exp <= a.Now().Unix() {
		return googleGrant{}, errors.New("Google's ID token isn't for this client")
	}
	return googleGrant{refresh: body.RefreshToken, access: body.AccessToken, sub: claims.Sub, email: claims.Email}, nil
}

// googleCalendars lists the account's calendars, at most maxGoogleCalendars.
func (a *App) googleCalendars(ctx context.Context, access string) ([]googleCalendar, error) {
	var all []googleCalendar
	page := ""
	for {
		q := url.Values{"maxResults": {"250"}}
		if page != "" {
			q.Set("pageToken", page)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Upstreams.GoogleAPI+"/calendar/v3/users/me/calendarList?"+q.Encode(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+access)
		res, err := googleClient.Do(req)
		if err != nil {
			return nil, err
		}
		var body struct {
			Items         []googleCalendar `json:"items"`
			NextPageToken string           `json:"nextPageToken"`
		}
		err = json.NewDecoder(http.MaxBytesReader(nil, res.Body, 1<<20)).Decode(&body)
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("Google wouldn't list its calendars (%s)", res.Status)
		}
		if err != nil {
			return nil, fmt.Errorf("Google's calendar list: %w", err)
		}
		all = append(all, body.Items...)
		if page = body.NextPageToken; page == "" || len(all) >= maxGoogleCalendars {
			return all[:min(len(all), maxGoogleCalendars)], nil
		}
	}
}

// revokeGoogle withdraws a token at Google. One Google already dropped counts as done.
func (a *App) revokeGoogle(ctx context.Context, token string) error {
	status, err := a.googlePost(ctx, "/revoke", url.Values{"token": {token}}, nil)
	if err == nil && status != http.StatusOK && status != http.StatusBadRequest { // 400: invalid_token
		err = fmt.Errorf("Google answered %d", status)
	}
	return err
}

// googlePost posts form to Google's OAuth endpoint path and decodes its JSON answer into out, if any.
func (a *App) googlePost(ctx context.Context, path string, form url.Values, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.Upstreams.GoogleOAuth+path, strings.NewReader(form.Encode()))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := googleClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	if out != nil {
		json.NewDecoder(http.MaxBytesReader(nil, res.Body, 1<<20)).Decode(out) // an error answer may not be JSON
	}
	return res.StatusCode, nil
}

// disconnectGoogle revokes a Google account's token at Google and deletes it
// with its calendars, their Member defaults and events.
func (a *App) disconnectGoogle(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if r.PostFormValue("confirm") == "" {
		a.settingsPage(w, r, http.StatusBadRequest, "Tick the box to confirm: disconnecting loses its calendars' default Members and every Member change made to its events.")
		return
	}
	var enc []byte
	if err := a.DB.QueryRowContext(r.Context(), "SELECT credentials_enc FROM integrations WHERE id = ? AND type = ?", id, googleType).Scan(&enc); errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	var tok googleToken
	plain, revokeErr := a.unseal(enc)
	if revokeErr == nil {
		json.Unmarshal(plain, &tok)
		revokeErr = a.revokeGoogle(r.Context(), tok.RefreshToken)
	}
	if _, err := a.exec(r.Context(), KindEvent, "DELETE FROM integrations WHERE id = ? AND type = ?", id, googleType); err != nil {
		a.serverError(w, r, err)
		return
	}
	if revokeErr != nil {
		// Still disconnected here: being stuck with an account you want gone is worse.
		a.Log.Warn("revoking a google token failed", "integration", id, "err", revokeErr)
		a.settingsPage(w, r, http.StatusOK, "Disconnected, but Google couldn't be told. Also remove GoCasa at https://myaccount.google.com/permissions.")
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// updateGoogleCalendar shows or hides a Google calendar and sets its default Members.
func (a *App) updateGoogleCalendar(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	r.ParseForm()
	err := a.inTx(r.Context(), func(tx *sql.Tx) error {
		if err := execOne(r.Context(), tx, "UPDATE source_calendars SET enabled = ? WHERE id = ? AND integration_id IN (SELECT id FROM integrations WHERE type = ?)",
			r.PostForm.Get("enabled") != "", id, googleType); err != nil {
			return err
		}
		// The form lists only active Members: an archived one's default stays for their return.
		if _, err := tx.ExecContext(r.Context(), "DELETE FROM source_calendar_members WHERE calendar_id = ? AND member_id IN (SELECT id FROM members WHERE NOT archived)", id); err != nil {
			return err
		}
		for _, m := range parseIDs(r.PostForm["member"]) {
			if _, err := tx.ExecContext(r.Context(), "INSERT OR IGNORE INTO source_calendar_members SELECT ?, id FROM members WHERE id = ? AND NOT archived", id, m); err != nil {
				return err
			}
		}
		return applyDefaultMembers(r.Context(), tx, id, nil)
	}, KindEvent)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	case err != nil:
		a.serverError(w, r, err)
	default:
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
	}
}

func (googleSync) Section(ctx context.Context, a *App, st map[int64]integrationStatus, members []web.Member) (templ.Component, error) {
	g, err := a.googleSettings(ctx, st)
	return web.GoogleSection(g, members), err
}

// googleSettings is the Google section of Settings.
func (a *App) googleSettings(ctx context.Context, st map[int64]integrationStatus) (web.Google, error) {
	g := web.Google{Configured: a.Config.GoogleClientID != "", RedirectURI: a.Config.GoogleRedirectURI}
	rows, err := a.DB.QueryContext(ctx, `SELECT i.id, i.name, c.id, c.name, c.color, c.enabled, c.read_only,
			(SELECT json_group_array(member_id) FROM source_calendar_members WHERE calendar_id = c.id)
		FROM integrations i LEFT JOIN source_calendars c ON c.integration_id = i.id
		WHERE i.type = ? ORDER BY i.name, i.id, c.name, c.id`, googleType)
	if err != nil {
		return g, err
	}
	defer rows.Close()
	for rows.Next() {
		var acct web.GoogleAccount
		var calID sql.NullInt64
		var name, color, members sql.NullString
		var enabled, readOnly sql.NullBool
		if err := rows.Scan(&acct.ID, &acct.Email, &calID, &name, &color, &enabled, &readOnly, &members); err != nil {
			return g, err
		}
		x := st[acct.ID]
		acct.Reconnect, acct.Failures, acct.LastError, acct.LastSuccess = x.Reconnect, x.Failures, x.LastError, x.LastSuccess
		if acct.Reconnect {
			acct.Failures = 0 // Reconnect needed says why it isn't syncing
		}
		if n := len(g.Accounts); n == 0 || g.Accounts[n-1].ID != acct.ID {
			g.Accounts = append(g.Accounts, acct)
		}
		if !calID.Valid {
			continue
		}
		c := web.GoogleCalendar{ID: calID.Int64, Name: name.String, Color: color.String, Enabled: enabled.Bool, ReadOnly: readOnly.Bool, Members: map[int64]bool{}}
		var ids []int64
		json.Unmarshal([]byte(members.String), &ids)
		for _, m := range ids {
			c.Members[m] = true
		}
		last := &g.Accounts[len(g.Accounts)-1]
		last.Calendars = append(last.Calendars, c)
	}
	return g, rows.Err()
}
