package app

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"
	"golang.org/x/crypto/bcrypt"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/DeRusto/goCasa/internal/web"
)

var (
	colorRE = regexp.MustCompile(`^#[0-9a-f]{6}$`)
	pinRE   = regexp.MustCompile(`^[0-9]{4,8}$`)
)

const errLastAdmin = "The Household needs at least one Admin."

// keepsAnAdmin is true for a Member whose demotion, archiving or deletion
// still leaves the Household an active Admin.
const keepsAnAdmin = "(role != 'admin' OR archived OR (SELECT count(*) FROM members WHERE role = 'admin' AND NOT archived) > 1)"

// memberForm is a validated Member form: name, colour, role and an optional
// PIN (bcrypt hash, nil when the PIN field was blank).
type memberForm struct {
	name, color, role string
	pinHash           []byte
}

func parseMemberForm(r *http.Request) (memberForm, string) {
	f := memberForm{
		name:  strings.TrimSpace(r.PostFormValue("name")),
		color: strings.ToLower(r.PostFormValue("color")),
		role:  r.PostFormValue("role"),
	}
	pin := r.PostFormValue("pin")
	switch {
	case f.name == "" || len([]rune(f.name)) > 50:
		return f, "A name is 1 to 50 characters."
	case !colorRE.MatchString(f.color):
		return f, "Pick a colour."
	case f.role != "admin" && f.role != "member":
		return f, "Pick a role."
	case pin != "" && f.role != "admin":
		return f, "Only Admins have a PIN."
	case pin != "" && !pinRE.MatchString(pin):
		return f, "A PIN is 4 to 8 digits."
	}
	if pin != "" {
		h, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
		if err != nil {
			return f, "Could not save the PIN."
		}
		f.pinHash = h
	}
	return f, ""
}

func (a *App) members(r *http.Request, adminsOnly bool) ([]web.Member, error) {
	q := "SELECT id, name, color, role, archived, avatar IS NOT NULL FROM members"
	if adminsOnly {
		q += " WHERE role = 'admin' AND NOT archived"
	}
	rows, err := a.DB.QueryContext(r.Context(), q+" ORDER BY name, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ms []web.Member
	for rows.Next() {
		var m web.Member
		if err := rows.Scan(&m.ID, &m.Name, &m.Color, &m.Role, &m.Archived, &m.HasAvatar); err != nil {
			return nil, err
		}
		ms = append(ms, m)
	}
	return ms, rows.Err()
}

func (a *App) render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	pill, err := a.weatherPill(r.Context())
	if err != nil {
		a.Log.Error("reading cached weather", "err", err) // the page still renders, without its pill
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := c.Render(web.WithWeatherPill(r.Context(), pill), w); err != nil {
		a.Log.Error("render failed", "path", r.URL.Path, "err", err)
	}
}

func (a *App) serverError(w http.ResponseWriter, r *http.Request, err error) {
	a.Log.Error("request failed", "path", r.URL.Path, "err", err)
	http.Error(w, "something went wrong", http.StatusInternalServerError)
}

// settingsPage shows first-run setup when there are no Members, the PIN pad
// when this device is locked, and Settings otherwise.
func (a *App) settingsPage(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	if unlockedAdmin(r.Context()) == 0 {
		var any bool
		if err := a.DB.QueryRowContext(r.Context(), "SELECT EXISTS (SELECT 1 FROM members)").Scan(&any); err != nil {
			a.serverError(w, r, err)
			return
		}
		if !any {
			a.render(w, r, status, web.Setup(errMsg))
			return
		}
		http.Redirect(w, r, "/lock", http.StatusSeeOther)
		return
	}
	ms, err := a.members(r, false)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	fs, err := a.feeds(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	g, err := a.googleSettings(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	wx, err := a.weatherSettings(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, status, web.Settings(ms, fs, g, wx, errMsg))
}

func (a *App) getSettings(w http.ResponseWriter, r *http.Request) {
	a.settingsPage(w, r, http.StatusOK, "")
}

// requireUnlock wraps a Settings action: a locked device goes to the PIN pad.
func (a *App) requireUnlock(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if unlockedAdmin(r.Context()) == 0 {
			http.Redirect(w, r, "/lock", http.StatusSeeOther)
			return
		}
		h(w, r)
	}
}

// postSetup creates the first Admin, only while there are no Members, and
// unlocks this device for them.
func (a *App) postSetup(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	r.PostForm.Set("role", "admin")
	f, msg := parseMemberForm(r)
	if msg == "" && f.pinHash == nil {
		msg = "The first Admin needs a PIN."
	}
	if msg != "" {
		a.settingsPage(w, r, http.StatusBadRequest, msg)
		return
	}
	res, err := a.DB.ExecContext(r.Context(), `INSERT INTO members (name, color, role, pin_hash, created_at)
		SELECT ?, ?, 'admin', ?, ? WHERE NOT EXISTS (SELECT 1 FROM members)`,
		f.name, f.color, f.pinHash, a.Now().Unix())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Redirect(w, r, "/lock", http.StatusSeeOther) // someone else finished setup first
		return
	}
	id, _ := res.LastInsertId()
	a.setUnlock(w, id)
	a.publish("created", "member", id)
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (a *App) getLock(w http.ResponseWriter, r *http.Request) { a.lockPage(w, r, http.StatusOK, "") }

func (a *App) lockPage(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	admins, err := a.members(r, true)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if len(admins) == 0 {
		http.Redirect(w, r, "/settings", http.StatusSeeOther) // first run
		return
	}
	a.render(w, r, status, web.Lock(admins, errMsg))
}

// Wrong PINs (spec §9): pinTries in a row block that Admin's PIN for pinBlock.
const (
	pinTries = 5
	pinBlock = 5 * time.Minute
)

var errPINBlocked = fmt.Sprintf("Too many wrong PINs: that Admin's PIN is blocked for %v minutes.", int(pinBlock.Minutes()))

func (a *App) postLock(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PostFormValue("member_id"), 10, 64)
	now := a.Now().Unix()
	// Count the try before the slow bcrypt check, so parallel guesses can't get
	// past the limit; the try that reaches it starts the block, which ends when
	// pinBlock passes or the PIN is reset. A crash mid-check counts as a wrong try.
	var hash []byte
	var nowBlocked bool
	err := a.DB.QueryRowContext(r.Context(), `UPDATE members SET
			pin_failures = CASE WHEN pin_failures + 1 >= ?2 THEN 0 ELSE pin_failures + 1 END,
			pin_blocked_until = CASE WHEN pin_failures + 1 >= ?2 THEN ?3 + ?4 ELSE pin_blocked_until END
		WHERE id = ?1 AND role = 'admin' AND NOT archived AND coalesce(pin_blocked_until, 0) <= ?3
		RETURNING pin_hash, coalesce(pin_blocked_until > ?3, 0)`,
		id, pinTries, now, int64(pinBlock/time.Second)).Scan(&hash, &nowBlocked)
	if errors.Is(err, sql.ErrNoRows) {
		err = a.DB.QueryRowContext(r.Context(), "SELECT 1 FROM members WHERE id = ? AND role = 'admin' AND NOT archived", id).Scan(new(int))
		if err == nil {
			a.lockPage(w, r, http.StatusTooManyRequests, errPINBlocked)
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			a.lockPage(w, r, http.StatusUnauthorized, "That PIN didn't match. Try again.")
			return
		}
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(r.PostFormValue("pin"))) != nil {
		if nowBlocked {
			a.lockPage(w, r, http.StatusTooManyRequests, errPINBlocked)
			return
		}
		a.lockPage(w, r, http.StatusUnauthorized, "That PIN didn't match. Try again.")
		return
	}
	if _, err := a.DB.ExecContext(r.Context(), "UPDATE members SET pin_failures = 0, pin_blocked_until = NULL WHERE id = ?", id); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.setUnlock(w, id)
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (a *App) postLockNow(w http.ResponseWriter, r *http.Request) {
	a.clearUnlock(w)
	http.Redirect(w, r, "/lock", http.StatusSeeOther)
}

func (a *App) createMember(w http.ResponseWriter, r *http.Request) {
	f, msg := parseMemberForm(r)
	if msg == "" && f.role == "admin" && f.pinHash == nil {
		msg = "An Admin needs a PIN."
	}
	if msg != "" {
		a.settingsPage(w, r, http.StatusBadRequest, msg)
		return
	}
	var id int64
	if err := a.DB.QueryRowContext(r.Context(), "INSERT INTO members (name, color, role, pin_hash, created_at) VALUES (?, ?, ?, ?, ?) RETURNING id",
		f.name, f.color, f.role, f.pinHash, a.Now().Unix()).Scan(&id); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.publish("created", "member", id)
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (a *App) updateMember(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var role string
	err := a.DB.QueryRowContext(r.Context(), "SELECT role FROM members WHERE id = ?", id).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	f, msg := parseMemberForm(r)
	if msg == "" && f.role == "admin" && role != "admin" && f.pinHash == nil {
		msg = "Making " + f.name + " an Admin needs a PIN."
	}
	if msg != "" {
		a.settingsPage(w, r, http.StatusBadRequest, msg)
		return
	}
	// One guarded statement, so another device's change between the read above
	// and this write can't demote the last Admin or leave an Admin without a PIN.
	res, err := a.DB.ExecContext(r.Context(), `UPDATE members SET name = ?1, color = ?2, role = ?3,
			pin_hash = CASE WHEN ?3 = 'admin' THEN coalesce(?4, pin_hash) END,
			-- A new PIN clears a wrong-PIN block: this is how an Admin resets another's.
			pin_failures = CASE WHEN ?4 IS NULL THEN pin_failures ELSE 0 END,
			pin_blocked_until = CASE WHEN ?4 IS NULL THEN pin_blocked_until END
		WHERE id = ?5
			AND (?3 = 'admin' OR `+keepsAnAdmin+`)
			AND (?3 != 'admin' OR ?4 IS NOT NULL OR pin_hash IS NOT NULL)`,
		f.name, f.color, f.role, f.pinHash, id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		msg := errLastAdmin
		if f.role == "admin" && f.pinHash == nil {
			msg = f.name + " is no longer an Admin; making them one needs a PIN."
		}
		a.settingsPage(w, r, http.StatusConflict, msg)
		return
	}
	a.publish("updated", "member", id)
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// deleteMember hard-deletes a Member with no history: history tables reference
// members with no ON DELETE action, so the foreign key refuses the rest.
func (a *App) deleteMember(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	res, err := a.DB.ExecContext(r.Context(), "DELETE FROM members WHERE id = ? AND "+keepsAnAdmin, id)
	var serr *sqlite.Error
	if errors.As(err, &serr) && serr.Code() == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY {
		a.settingsPage(w, r, http.StatusConflict, "They have history, so they can't be deleted. Archive them instead.")
		return
	}
	a.guardedMemberChange(w, r, "deleted", id, res, err)
}

// setArchived archives (hides, keeping their history) or restores a Member.
func (a *App) setArchived(archived bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
		res, err := a.DB.ExecContext(r.Context(), "UPDATE members SET archived = ? WHERE id = ? AND (NOT ? OR "+keepsAnAdmin+")", archived, id, archived)
		a.guardedMemberChange(w, r, "updated", id, res, err)
	}
}

// guardedMemberChange finishes a change to Member id that keepsAnAdmin guarded:
// no row changed means no such Member, or the last active Admin.
func (a *App) guardedMemberChange(w http.ResponseWriter, r *http.Request, typ string, id int64, res sql.Result, err error) {
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var exists bool
		if err := a.DB.QueryRowContext(r.Context(), "SELECT EXISTS (SELECT 1 FROM members WHERE id = ?)", id).Scan(&exists); err != nil {
			a.serverError(w, r, err)
			return
		}
		if !exists {
			http.NotFound(w, r)
			return
		}
		a.settingsPage(w, r, http.StatusConflict, errLastAdmin)
		return
	}
	a.publish(typ, "member", id)
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// ResetPIN gives the active Admin named who (name, ASCII case ignored, or id) a new
// random 6-digit PIN and clears any wrong-PIN block. It is `gocasa reset-pin`,
// for when the last Admin forgets their PIN.
func (a *App) ResetPIN(ctx context.Context, who string) (string, error) {
	// An exact id wins over a name, so every Admin can be picked by id.
	rows, err := a.DB.QueryContext(ctx, `SELECT id FROM members WHERE role = 'admin' AND NOT archived
		AND (CAST(id AS TEXT) = ?1 OR (lower(name) = lower(?1) AND NOT EXISTS (
			SELECT 1 FROM members WHERE role = 'admin' AND NOT archived AND CAST(id AS TEXT) = ?1)))`, strings.TrimSpace(who))
	if err != nil {
		return "", err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(ids) != 1 {
		return "", fmt.Errorf("%d active Admins match %q; give one Admin's name or id", len(ids), who)
	}
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	pin := fmt.Sprintf("%06d", n)
	hash, err := bcrypt.GenerateFromPassword([]byte(pin), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	_, err = a.DB.ExecContext(ctx, "UPDATE members SET pin_hash = ?, pin_failures = 0, pin_blocked_until = NULL WHERE id = ?", hash, ids[0])
	return pin, err
}
