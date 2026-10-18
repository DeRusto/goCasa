package app

import (
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/a-h/templ"
	"golang.org/x/crypto/bcrypt"

	"github.com/DeRusto/goCasa/internal/web"
)

var (
	colorRE = regexp.MustCompile(`^#[0-9a-f]{6}$`)
	pinRE   = regexp.MustCompile(`^[0-9]{4,8}$`)
)

const errLastAdmin = "The Household needs at least one Admin."

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
	q := "SELECT id, name, color, role FROM members"
	if adminsOnly {
		q += " WHERE role = 'admin'"
	}
	rows, err := a.DB.QueryContext(r.Context(), q+" ORDER BY name, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ms []web.Member
	for rows.Next() {
		var m web.Member
		if err := rows.Scan(&m.ID, &m.Name, &m.Color, &m.Role); err != nil {
			return nil, err
		}
		ms = append(ms, m)
	}
	return ms, rows.Err()
}

func (a *App) render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil {
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
	a.render(w, r, status, web.Settings(ms, errMsg))
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

func (a *App) postLock(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PostFormValue("member_id"), 10, 64)
	var hash []byte
	err := a.DB.QueryRowContext(r.Context(), "SELECT pin_hash FROM members WHERE id = ? AND role = 'admin'", id).Scan(&hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		a.serverError(w, r, err)
		return
	}
	if err != nil || bcrypt.CompareHashAndPassword(hash, []byte(r.PostFormValue("pin"))) != nil {
		a.lockPage(w, r, http.StatusUnauthorized, "That PIN didn't match. Try again.")
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
	if _, err := a.DB.ExecContext(r.Context(), "INSERT INTO members (name, color, role, pin_hash, created_at) VALUES (?, ?, ?, ?, ?)",
		f.name, f.color, f.role, f.pinHash, a.Now().Unix()); err != nil {
		a.serverError(w, r, err)
		return
	}
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
			pin_hash = CASE WHEN ?3 = 'admin' THEN coalesce(?4, pin_hash) END
		WHERE id = ?5
			AND (?3 = 'admin' OR role != 'admin' OR (SELECT count(*) FROM members WHERE role = 'admin') > 1)
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
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (a *App) deleteMember(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	res, err := a.DB.ExecContext(r.Context(), `DELETE FROM members
		WHERE id = ? AND (role != 'admin' OR (SELECT count(*) FROM members WHERE role = 'admin') > 1)`, id)
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
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}
