package app

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"encoding/json"
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

var integrationTypes = map[string]Integration{"ical": icalFeed{}, googleType: googleSync{}, openMeteoType: openMeteo{}, mealieType: mealie{}, immichType: immich{}}

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
	downAfter   = 3 // failures in a row before an integration is Down
	maxErrRunes = 500
)

// integrations owns every integrations row's health (GLOSSARY: Integration,
// Failing, Down): syncing one under its lock, recording how it went, and
// connecting one. Each integration type keeps only its own Sync.
type integrations struct {
	app   *App
	locks sync.Map // integration id → *sync.Mutex
}

// integrationHealth is how an integration's syncs are going.
type integrationHealth struct {
	Failing     bool // at least one failure in a row
	Down        bool // downAfter or more: Mirrored lists go read-only, calendars show a stale notice
	Reconnect   bool // Reconnect needed: no syncs until an Admin reconnects it
	Failures    int
	LastError   string
	LastSuccess sql.NullInt64
}

// SQL for an integrations row t (a table name or alias) that is Failing, Down
// or Reconnect needed, for queries joining it.
func failingSQL(t string) string   { return t + ".consecutive_failures > 0" }
func downSQL(t string) string      { return fmt.Sprintf("%s.consecutive_failures >= %d", t, downAfter) }
func reconnectSQL(t string) string { return t + ".status = 'reconnect_needed'" }

// Health is integration id's health, or sql.ErrNoRows.
func (s *integrations) Health(ctx context.Context, id int64) (integrationHealth, error) {
	var h integrationHealth
	err := s.app.DB.QueryRowContext(ctx, `SELECT `+failingSQL("i")+`, `+downSQL("i")+`, `+reconnectSQL("i")+`, i.consecutive_failures, i.last_error, i.last_success_at
		FROM integrations i WHERE i.id = ?`, id).Scan(&h.Failing, &h.Down, &h.Reconnect, &h.Failures, &h.LastError, &h.LastSuccess)
	return h, err
}

// WithLock runs fn while no sync of integration id runs: for a write through
// it, which must not race a sync writing back the cursor it read.
func (s *integrations) WithLock(id int64, fn func() error) error {
	mu, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	return fn()
}

// RecordFailure records a write through integration id that failed: it is
// Down until its next good sync. Call it inside WithLock, so a write waiting
// there sees it.
func (s *integrations) RecordFailure(ctx context.Context, id int64, err error) error {
	_, rerr := s.app.DB.ExecContext(context.WithoutCancel(ctx), "UPDATE integrations SET consecutive_failures = max(consecutive_failures, ?), last_error = ? WHERE id = ?",
		downAfter, cutError(err), id)
	return rerr
}

// PollSoon makes integration id due on the next scheduler tick.
func (s *integrations) PollSoon(ctx context.Context, id int64) error {
	_, err := s.app.DB.ExecContext(ctx, "UPDATE integrations SET next_sync_at = min(next_sync_at, ?) WHERE id = ?", s.app.Now().Unix(), id)
	return err
}

// NeedsReconnect marks integration id Reconnect needed, unless its
// credentials are no longer sealed (reconnected meanwhile).
func (s *integrations) NeedsReconnect(ctx context.Context, id int64, sealed []byte) error {
	_, err := s.app.DB.ExecContext(ctx, "UPDATE integrations SET status = 'reconnect_needed' WHERE id = ? AND credentials_enc IS ?", id, sealed)
	return err
}

// Reconnected ends integration id's Reconnect needed and any backoff, in tx:
// it syncs straight away.
func (s *integrations) Reconnected(ctx context.Context, tx *sql.Tx, id int64) error {
	_, err := tx.ExecContext(ctx, "UPDATE integrations SET status = 'ok', consecutive_failures = 0, last_error = '', next_sync_at = ? WHERE id = ?", s.app.Now().Unix(), id)
	return err
}

func cutError(err error) string {
	msg := []rune(err.Error())
	return string(msg[:min(len(msg), maxErrRunes)])
}

// connection is an integration to Connect, one per typ.
type connection struct {
	typ, name     string // name as Settings shows it
	url           string // its address, stored unsealed
	secret        string
	missingSecret string                                 // refuses an empty secret
	cfg           any                                    // config_json
	interval      int                                    // seconds between syncs
	check         func(ctx context.Context) (msg string) // probes the upstream; msg refuses it
}

// Connect checks c and adds it, syncing it once (its outcome shows in
// Settings). msg refuses it, for the person.
func (s *integrations) Connect(ctx context.Context, c connection) (msg string, err error) {
	switch u := parseUpstreamURL(c.url); {
	case u == nil:
		return c.name + "'s address starts with https:// or http://.", nil
	case u.User != nil: // the address is stored unsealed
		return "Leave the user name and password out of " + c.name + "'s address.", nil
	case c.secret == "":
		return c.missingSecret, nil
	}
	taken := c.name + " is already connected. Disconnect it first."
	var exists bool
	if err := s.app.DB.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM integrations WHERE type = ?)", c.typ).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return taken, nil
	}
	if msg := c.check(ctx); msg != "" {
		return msg, nil
	}
	enc, err := s.app.seal([]byte(c.secret))
	if err != nil {
		return "", err
	}
	cfgJSON, _ := json.Marshal(c.cfg)
	var id int64
	// One statement, so a connect racing this one since the check above can't add a second.
	err = s.app.DB.QueryRowContext(ctx, `INSERT INTO integrations (type, name, config_json, credentials_enc, sync_interval_seconds, next_sync_at)
		SELECT ?1, ?2, ?3, ?4, ?5, ?6 WHERE NOT EXISTS (SELECT 1 FROM integrations WHERE type = ?1) RETURNING id`,
		c.typ, c.name, string(cfgJSON), enc, c.interval, s.app.Now().Add(syncTimeout).Unix()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return taken, nil
	} else if err != nil {
		return "", err
	}
	_, err = s.Run(ctx, id)
	return "", err
}

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
		if _, err := a.integrations.Run(ctx, id); err != nil && !errors.Is(err, sql.ErrNoRows) {
			a.Log.Error("recording integration sync failed", "integration", id, "err", err)
		}
	}
	return nil
}

// Run syncs integration id under its lock and records how it went: synced
// says the sync worked. err is only a recording that failed.
func (s *integrations) Run(ctx context.Context, id int64) (synced bool, err error) {
	// One sync of an integration at a time: each writes back the cursor it read.
	err = s.WithLock(id, func() error {
		synced, err = s.run(ctx, id)
		return err
	})
	return synced, err
}

func (s *integrations) run(ctx context.Context, id int64) (bool, error) {
	a := s.app
	var typ, cursor string
	var enc []byte
	var interval, failures int64
	if err := a.DB.QueryRowContext(ctx, "SELECT type, credentials_enc, sync_cursor, sync_interval_seconds, consecutive_failures FROM integrations WHERE id = ?", id).
		Scan(&typ, &enc, &cursor, &interval, &failures); err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, syncTimeout)
	err := a.runSync(ctx, typ, enc, integrationRow{id: id, cursor: cursor, sealed: enc})
	cancel()
	now := a.Now()
	if err == nil {
		_, err = a.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE integrations SET last_sync_at = ?1, last_success_at = ?1, consecutive_failures = 0, last_error = '', next_sync_at = ?2 WHERE id = ?3`,
			now.Unix(), now.Add(time.Duration(interval)*time.Second).Unix(), id)
		return err == nil, err
	}
	a.Log.Warn("integration sync failed", "integration", id, "type", typ, "err", err)
	// 1, 2, 4… minutes, at most maxBackoff.
	delay := min(time.Minute<<min(failures, 20), maxBackoff)
	if errors.As(err, new(transientError)) {
		delay = time.Duration(interval) * time.Second
	}
	_, err = a.DB.ExecContext(context.WithoutCancel(ctx), `UPDATE integrations SET last_sync_at = ?, consecutive_failures = consecutive_failures + 1, last_error = ?, next_sync_at = ? WHERE id = ?`,
		now.Unix(), cutError(err), now.Add(delay).Unix(), id)
	a.publish("updated", "integration", id) // an open calendar page may now show a stale-sync notice
	return false, err
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

// lastSync is when an integration last synced, as the pages show it; clock is a clockLayout.
func (a *App) lastSync(ctx context.Context, at sql.NullInt64, clock string) string {
	if !at.Valid {
		return "never"
	}
	return time.Unix(at.Int64, 0).In(a.location(ctx)).Format("2 Jan 2006 " + clock)
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
	clock := a.clock(ctx)
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
		f.LastSuccess = a.lastSync(ctx, ok, clock)
		fs = append(fs, f)
	}
	return fs, rows.Err()
}

// addFeed adds an iCal/webcal feed with its own read-only calendar and syncs it straight away.
func (a *App) addFeed(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	color := strings.ToLower(r.PostFormValue("color"))
	raw := strings.TrimSpace(r.PostFormValue("url"))
	switch {
	case name == "" || len([]rune(name)) > 50:
		a.settingsPage(w, r, http.StatusBadRequest, "A feed's name is 1 to 50 characters.")
		return
	case !colorRE.MatchString(color):
		a.settingsPage(w, r, http.StatusBadRequest, "Pick a colour.")
		return
	case parseUpstreamURL(feedURL(raw)) == nil:
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
		_, err = a.integrations.Run(r.Context(), id) // its outcome shows in Settings
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

// parseUpstreamURL is raw parsed when it is an http(s) address with a host, else nil.
func parseUpstreamURL(raw string) *url.URL {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil
	}
	return u
}

// connected answers a Connect: its refusal on Settings, else back to Settings.
func (a *App) connected(w http.ResponseWriter, r *http.Request, msg string, err error) {
	switch {
	case err != nil:
		a.serverError(w, r, err)
	case msg != "":
		a.settingsPage(w, r, http.StatusBadRequest, msg)
	default:
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
	}
}
