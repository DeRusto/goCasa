package app

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
)

// Integration is one kind of sync source (spec §10), named by its
// integrations row's type. Sync brings that row's data up to date; an error
// or panic is recorded on the row and backs it off, never touching the rest.
type Integration interface {
	Sync(ctx context.Context, a *App, in integrationRow) error
}

var integrationTypes = map[string]Integration{"ical": icalFeed{}, googleType: googleSync{}}

// integrationRow is what Sync gets from its row.
type integrationRow struct {
	id          int64
	credentials []byte // decrypted; nil if none
	cursor      string // sync_cursor; Sync writes it back itself, in its own transaction
	sealed      []byte // credentials_enc as read: a reconnect meanwhile changes it
}

const (
	syncTimeout = 2 * time.Minute
	maxBackoff  = 6 * time.Hour
)

// syncDue syncs each enabled integration whose next_sync_at has come, one at a
// time; a Google account needing a reconnect waits for it.
func (a *App) syncDue(ctx context.Context, now time.Time) error {
	rows, err := a.DB.QueryContext(ctx, "SELECT id FROM integrations WHERE enabled AND status = 'ok' AND next_sync_at <= ? ORDER BY next_sync_at, id", now.Unix())
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		// One that can't be recorded (e.g. removed meanwhile) mustn't hold up the rest.
		if err := a.syncIntegration(ctx, id); err != nil && !errors.Is(err, sql.ErrNoRows) {
			a.Log.Error("recording integration sync failed", "integration", id, "err", err)
		}
	}
	return nil
}

// syncIntegration runs integration id's Sync and records how it went. Its
// error is only one recording that failed.
func (a *App) syncIntegration(ctx context.Context, id int64) error {
	// One sync of an integration at a time: each writes back the cursor it read.
	mu, _ := a.syncLocks.LoadOrStore(id, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	var typ, cursor string
	var enc []byte
	var interval, failures int64
	if err := a.DB.QueryRowContext(ctx, "SELECT type, credentials_enc, sync_cursor, sync_interval_seconds, consecutive_failures FROM integrations WHERE id = ?", id).
		Scan(&typ, &enc, &cursor, &interval, &failures); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, syncTimeout)
	err := a.runSync(ctx, typ, enc, integrationRow{id: id, cursor: cursor, sealed: enc})
	cancel()
	now := a.Now()
	if err == nil {
		_, err = a.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE integrations SET last_sync_at = ?1, last_success_at = ?1, consecutive_failures = 0, last_error = '', next_sync_at = ?2 WHERE id = ?3`,
			now.Unix(), now.Add(time.Duration(interval)*time.Second).Unix(), id)
		return err
	}
	a.Log.Warn("integration sync failed", "integration", id, "type", typ, "err", err)
	// 1, 2, 4… minutes, at most maxBackoff.
	delay := min(time.Minute<<min(failures, 20), maxBackoff)
	if errors.As(err, new(transientError)) {
		delay = time.Duration(interval) * time.Second
	}
	msg := []rune(err.Error())
	_, err = a.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE integrations SET last_sync_at = ?, consecutive_failures = consecutive_failures + 1, last_error = ?, next_sync_at = ? WHERE id = ?`,
		now.Unix(), string(msg[:min(len(msg), 500)]), now.Add(delay).Unix(), id)
	a.publish("updated", "integration", id) // an open calendar page may now show a stale-sync notice
	return err
}

func (a *App) runSync(ctx context.Context, typ string, enc []byte, in integrationRow) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("crashed: %v", p)
		}
	}()
	impl, ok := integrationTypes[typ]
	if !ok {
		return fmt.Errorf("unknown integration type %q", typ)
	}
	if enc != nil {
		if in.credentials, err = a.unseal(enc); err != nil {
			return errors.New("can't decrypt its credentials: was APP_ENCRYPTION_KEY changed? Remove it and add it again")
		}
	}
	err = impl.Sync(ctx, a, in)
	// A url.Error repeats the URL, which may hold a feed's secret token.
	if ue := (*url.Error)(nil); errors.As(err, &ue) {
		err = ue.Err
	}
	return err
}

// seal encrypts credentials with APP_ENCRYPTION_KEY (spec §9): AES-256-GCM, its random nonce first.
func (a *App) seal(plain []byte) ([]byte, error) {
	gcm, err := a.gcm()
	if err != nil {
		return nil, err
	}
	return gcm.Seal(nil, nil, plain, nil), nil
}

func (a *App) unseal(sealed []byte) ([]byte, error) {
	gcm, err := a.gcm()
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nil, sealed, nil)
}

func (a *App) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(a.Config.EncryptionKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCMWithRandomNonce(block)
}

// lastSync is when an integration last synced, as the pages show it.
func (a *App) lastSync(at sql.NullInt64) string {
	if !at.Valid {
		return "never"
	}
	return time.Unix(at.Int64, 0).In(a.Config.Location).Format("2 Jan 2006 15:04")
}

// syncNow syncs every integration that can sync, due or not.
func (a *App) syncNow(w http.ResponseWriter, r *http.Request) {
	// Not cancelled by a closed tab: a sync cut short counts as a failure.
	if err := a.syncDue(context.WithoutCancel(r.Context()), time.Unix(1<<62, 0)); err != nil { // every next_sync_at is before then
		a.serverError(w, r, err)
		return
	}
	back := r.PostFormValue("back")
	if !strings.HasPrefix(back, "/calendar?") { // only back to a calendar view, never off-site
		back = "/calendar"
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

// feeds is the iCal feeds as Settings shows them.
func (a *App) feeds(ctx context.Context) ([]web.Feed, error) {
	rows, err := a.DB.QueryContext(ctx, `SELECT i.id, i.name, c.color, i.last_success_at, i.consecutive_failures, i.last_error
		FROM integrations i JOIN source_calendars c ON c.integration_id = i.id WHERE i.type = 'ical' ORDER BY i.name, i.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fs []web.Feed
	for rows.Next() {
		var f web.Feed
		var ok sql.NullInt64
		if err := rows.Scan(&f.ID, &f.Name, &f.Color, &ok, &f.Failures, &f.LastError); err != nil {
			return nil, err
		}
		f.LastSuccess = a.lastSync(ok)
		fs = append(fs, f)
	}
	return fs, rows.Err()
}

// addFeed adds an iCal/webcal feed with its own read-only calendar and syncs it straight away.
func (a *App) addFeed(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	color := strings.ToLower(r.PostFormValue("color"))
	raw := strings.TrimSpace(r.PostFormValue("url"))
	u, err := url.Parse(feedURL(raw))
	switch {
	case name == "" || len([]rune(name)) > 50:
		a.settingsPage(w, r, http.StatusBadRequest, "A feed's name is 1 to 50 characters.")
		return
	case !colorRE.MatchString(color):
		a.settingsPage(w, r, http.StatusBadRequest, "Pick a colour.")
		return
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		a.settingsPage(w, r, http.StatusBadRequest, "A feed's address starts with webcal://, https:// or http://.")
		return
	}
	enc, err := a.seal([]byte(raw))
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	var id int64
	err = a.inTx(r.Context(), func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(r.Context(), "INSERT INTO integrations (type, name, credentials_enc, next_sync_at) VALUES ('ical', ?, ?, ?) RETURNING id",
			name, enc, a.Now().Add(syncTimeout).Unix()).Scan(&id); err != nil { // not due until the sync below has had its time
			return err
		}
		_, err := tx.ExecContext(r.Context(), "INSERT INTO source_calendars (integration_id, name, color, read_only) VALUES (?, ?, ?, 1)", id, name, color)
		return err
	})
	if err == nil {
		err = a.syncIntegration(r.Context(), id) // its outcome shows in Settings
	}
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// removeFeed deletes an iCal feed with its calendar and events.
func (a *App) removeFeed(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if _, err := a.DB.ExecContext(r.Context(), "DELETE FROM integrations WHERE id = ? AND type = 'ical'", id); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.publish("deleted", "calendar", id)
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}
