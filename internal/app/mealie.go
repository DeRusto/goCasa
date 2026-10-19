package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
)

// mealie mirrors every shopping list of one Mealie household (spec §10):
// Mealie first, no queue. Its credentials are the Bearer token, its
// config_json the instance's address.
type mealie struct{}

const (
	mealieType     = "mealie"
	mealiePoll     = 60 // seconds between full pulls
	mealieDownPoll = 3  // failed polls in a row before the lists go read-only
	mealieLists    = "/api/households/shopping/lists"
	mealieItems    = "/api/households/shopping/items"
)

var (
	mealieClient     = feedClient
	errMealieGone    = errors.New("not found in Mealie")
	errMealieRemoved = "That item was removed in Mealie."
)

type mealieConfig struct {
	URL string `json:"url"`
}

// mealieConn is what a call to Mealie needs.
type mealieConn struct {
	id    int64
	base  string
	token string
}

// call sends body (if any) as JSON to Mealie and decodes its answer into out
// (if any). A 404 is errMealieGone; any other failure is transient, so polls
// keep their 60 s pace.
func (c mealieConn) call(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := mealieClient.Do(req)
	if err != nil {
		return transient(err)
	}
	defer res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound:
		return errMealieGone
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return errors.New("Mealie refused the API token")
	case res.StatusCode/100 != 2:
		return transient(fmt.Errorf("Mealie answered %s", res.Status))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 10<<20)).Decode(out); err != nil {
		return transient(fmt.Errorf("Mealie's answer isn't JSON: %w", err))
	}
	return nil
}

// mealieItem is a Mealie shopping-list item: its raw JSON, kept whole in
// extra_json, and the fields GoCasa reads from it.
type mealieItem struct {
	raw            json.RawMessage
	ID             string  `json:"id"`
	ShoppingListID string  `json:"shoppingListId"`
	Display        string  `json:"display"`
	Note           string  `json:"note"`
	Checked        bool    `json:"checked"`
	Position       float64 `json:"position"`
}

func (it *mealieItem) UnmarshalJSON(b []byte) error {
	type plain mealieItem
	if err := json.Unmarshal(b, (*plain)(it)); err != nil {
		return err
	}
	it.raw = append(json.RawMessage(nil), b...)
	return nil
}

// name is how GoCasa shows it: Mealie's display, cut to fit.
func (it mealieItem) name() string {
	n := strings.TrimSpace(it.Display)
	if n == "" {
		n = strings.TrimSpace(it.Note)
	}
	if n == "" {
		n = "(no name)"
	}
	return cut(n, 200)
}

func cut(s string, max int) string {
	if r := []rune(s); len(r) > max {
		return string(r[:max])
	}
	return s
}

// mealieCollection is what Mealie answers an item write with.
type mealieCollection struct {
	Created []mealieItem `json:"createdItems"`
	Updated []mealieItem `json:"updatedItems"`
	Deleted []mealieItem `json:"deletedItems"`
}

type mealieList struct {
	ID    string       `json:"id"`
	Name  string       `json:"name"`
	Items []mealieItem `json:"listItems"`
}

func (mealie) Sync(ctx context.Context, a *App, in integrationRow) error {
	c, err := a.mealieConn(ctx, in.id, in.credentials)
	if err != nil {
		return err
	}
	var page struct {
		Items []mealieList `json:"items"`
	}
	if err := c.call(ctx, http.MethodGet, mealieLists+"?perPage=-1", nil, &page); err != nil {
		return err
	}
	var lists []mealieList
	for _, l := range page.Items {
		var full mealieList
		if err := c.call(ctx, http.MethodGet, mealieLists+"/"+url.PathEscape(l.ID), nil, &full); errors.Is(err, errMealieGone) {
			continue // deleted since the summary
		} else if err != nil {
			return err
		}
		lists = append(lists, full)
	}
	return a.inTx(ctx, func(tx *sql.Tx) error {
		keep := []any{in.id}
		for _, l := range lists {
			id, err := upsertMealieList(ctx, tx, in.id, l)
			if err != nil {
				return err
			}
			if err := replaceMealieItems(ctx, tx, id, l.Items); err != nil {
				return err
			}
			keep = append(keep, l.ID)
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM shopping_lists WHERE integration_id = ? AND external_id NOT IN ("+placeholders(len(keep)-1)+")", keep...)
		if err == nil {
			a.publish("updated", "shopping_list", 0)
		}
		return err
	})
}

// placeholders is n comma-separated ?s for an IN list; with none, an empty set.
func placeholders(n int) string {
	if n == 0 {
		return "SELECT NULL WHERE 0"
	}
	return strings.Repeat("?, ", n-1) + "?"
}

func upsertMealieList(ctx context.Context, tx *sql.Tx, integration int64, l mealieList) (int64, error) {
	name := cut(strings.TrimSpace(l.Name), 50)
	if name == "" {
		name = "Mealie list"
	}
	var id int64
	err := tx.QueryRowContext(ctx, "SELECT id FROM shopping_lists WHERE integration_id = ? AND external_id = ?", integration, l.ID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `INSERT INTO shopping_lists (name, integration_id, external_id, position)
			VALUES (?, ?, ?, (SELECT coalesce(max(position), -1) + 1 FROM shopping_lists)) RETURNING id`, name, integration, l.ID).Scan(&id)
		return id, err
	}
	if err != nil {
		return 0, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE shopping_lists SET name = ? WHERE id = ?", name, id)
	return id, err
}

// replaceMealieItems makes list hold exactly items, by Mealie id.
func replaceMealieItems(ctx context.Context, tx *sql.Tx, list int64, items []mealieItem) error {
	keep := []any{list}
	for _, it := range items {
		if err := upsertMealieItem(ctx, tx, list, it); err != nil {
			return err
		}
		keep = append(keep, it.ID)
	}
	_, err := tx.ExecContext(ctx, "DELETE FROM shopping_items WHERE list_id = ? AND external_id NOT IN ("+placeholders(len(keep)-1)+")", keep...)
	return err
}

func upsertMealieItem(ctx context.Context, tx *sql.Tx, list int64, it mealieItem) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO shopping_items (list_id, external_id, note, checked, position, extra_json) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (list_id, external_id) WHERE external_id IS NOT NULL DO UPDATE SET
			note = excluded.note, checked = excluded.checked, position = excluded.position, extra_json = excluded.extra_json`,
		list, it.ID, it.name(), it.Checked, int64(it.Position), string(it.raw))
	return err
}

// mealieConn reads integration id's address; token is its decrypted credentials.
func (a *App) mealieConn(ctx context.Context, id int64, token []byte) (mealieConn, error) {
	var raw string
	if err := a.DB.QueryRowContext(ctx, "SELECT config_json FROM integrations WHERE id = ?", id).Scan(&raw); err != nil {
		return mealieConn{}, err
	}
	var cfg mealieConfig
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return mealieConn{}, err
	}
	return mealieConn{id: id, base: cfg.URL, token: string(token)}, nil
}

// mealieVersionOK says whether Mealie's /api/app/about version is v2.0 or later.
func mealieVersionOK(v string) bool {
	major, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimSpace(v), "v"), ".")
	n, err := strconv.Atoi(major)
	return err == nil && n >= 2
}

// connectMealie checks the instance is Mealie v2.0+, then adds it and pulls its lists.
func (a *App) connectMealie(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimRight(strings.TrimSpace(r.PostFormValue("url")), "/")
	token := strings.TrimSpace(r.PostFormValue("token"))
	u, err := url.Parse(raw)
	switch {
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		a.settingsPage(w, r, http.StatusBadRequest, "Mealie's address starts with https:// or http://.")
		return
	case token == "":
		a.settingsPage(w, r, http.StatusBadRequest, "Paste an API token from Mealie (Profile > API Tokens).")
		return
	}
	var exists bool
	if err := a.DB.QueryRowContext(r.Context(), "SELECT EXISTS (SELECT 1 FROM integrations WHERE type = ?)", mealieType).Scan(&exists); err != nil {
		a.serverError(w, r, err)
		return
	}
	if exists {
		a.settingsPage(w, r, http.StatusBadRequest, "Mealie is already connected. Disconnect it first.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var about struct {
		Version string `json:"version"`
	}
	if err := (mealieConn{base: raw, token: token}).call(ctx, http.MethodGet, "/api/app/about", nil, &about); err != nil {
		a.settingsPage(w, r, http.StatusBadRequest, "Couldn't reach Mealie at that address.")
		return
	}
	if !mealieVersionOK(about.Version) {
		a.settingsPage(w, r, http.StatusBadRequest, fmt.Sprintf("GoCasa needs Mealie v2.0 or later; this one is %q.", about.Version))
		return
	}
	enc, err := a.seal([]byte(token))
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	cfg, _ := json.Marshal(mealieConfig{URL: raw})
	var id int64
	if err := a.DB.QueryRowContext(r.Context(), `INSERT INTO integrations (type, name, config_json, credentials_enc, sync_interval_seconds, next_sync_at)
		VALUES (?, 'Mealie', ?, ?, ?, ?) RETURNING id`, mealieType, string(cfg), enc, mealiePoll, a.Now().Add(syncTimeout).Unix()).Scan(&id); err != nil {
		a.serverError(w, r, err)
		return
	}
	if err := a.syncIntegration(r.Context(), id); err != nil { // its outcome shows in Settings
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// disconnectMealie removes Mealie and its Mirrored lists from GoCasa; Mealie keeps them.
func (a *App) disconnectMealie(w http.ResponseWriter, r *http.Request) {
	if _, err := a.DB.ExecContext(r.Context(), "DELETE FROM integrations WHERE type = ?", mealieType); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.publish("deleted", "shopping_list", 0)
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// hideMealieList shows or hides a Mirrored list.
func (a *App) hideMealieList(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	hidden := r.PostFormValue("hidden") == "on"
	if _, err := a.DB.ExecContext(r.Context(), "UPDATE shopping_lists SET hidden = ? WHERE id = ? AND integration_id IS NOT NULL", hidden, id); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.publish("updated", "shopping_list", id)
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// mealieSettings is the Mealie section of Settings; nil when not connected.
func (a *App) mealieSettings(ctx context.Context) (*web.Mealie, error) {
	var m web.Mealie
	var cfg string
	var ok sql.NullInt64
	err := a.DB.QueryRowContext(ctx, `SELECT id, config_json, last_success_at, consecutive_failures, last_error FROM integrations WHERE type = ?`, mealieType).
		Scan(&m.ID, &cfg, &ok, &m.Failures, &m.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c mealieConfig
	json.Unmarshal([]byte(cfg), &c)
	m.URL, m.LastSuccess = c.URL, a.lastSync(ok)
	rows, err := a.DB.QueryContext(ctx, "SELECT id, name, hidden FROM shopping_lists WHERE integration_id = ? ORDER BY position, id", m.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var l web.MealieList
		if err := rows.Scan(&l.ID, &l.Name, &l.Hidden); err != nil {
			return nil, err
		}
		m.Lists = append(m.Lists, l)
	}
	return &m, rows.Err()
}

// mirror is a Mirrored list, ready for a write through Mealie.
type mirror struct {
	c    mealieConn
	list int64
	ext  string // the list's Mealie id
	down bool   // can't reach Mealie: read-only until the next good poll
}

// mirrorOf is list as a Mirrored list; nil for a local or missing list.
func (a *App) mirrorOf(ctx context.Context, list int64) (*mirror, error) {
	m := &mirror{list: list}
	var enc []byte
	var failures int
	err := a.DB.QueryRowContext(ctx, `SELECT l.external_id, i.id, i.credentials_enc, i.consecutive_failures
		FROM shopping_lists l JOIN integrations i ON i.id = l.integration_id WHERE l.id = ?`, list).Scan(&m.ext, &m.c.id, &enc, &failures)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	token, err := a.unseal(enc)
	if err != nil {
		return nil, err
	}
	if m.c, err = a.mealieConn(ctx, m.c.id, token); err != nil {
		return nil, err
	}
	m.down = failures >= mealieDownPoll
	return m, nil
}

const mealieUnreachable = "GoCasa can't reach Mealie, so its lists show their last contents and can't be changed until it answers again."

// mealieWrite runs write against Mealie for a request on Mirrored list m and
// answers it: a gone item re-pulls the list, any other failure makes the
// lists read-only until the next good poll.
func (a *App) mealieWrite(w http.ResponseWriter, r *http.Request, m *mirror, write func(ctx context.Context) error) {
	if m.down {
		a.listsPage(w, r, http.StatusServiceUnavailable, mealieUnreachable)
		return
	}
	mu, _ := a.syncLocks.LoadOrStore(m.c.id, &sync.Mutex{}) // not mid-poll
	mu.(*sync.Mutex).Lock()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	err := write(ctx)
	if errors.Is(err, errMealieGone) {
		if err = a.pullMealieList(ctx, m); err == nil {
			err = errMealieGone
		}
	}
	cancel()
	mu.(*sync.Mutex).Unlock()
	// Poll soon after every write, on the next scheduler tick.
	if _, err := a.DB.ExecContext(r.Context(), "UPDATE integrations SET next_sync_at = min(next_sync_at, ?) WHERE id = ?", a.Now().Unix(), m.c.id); err != nil {
		a.Log.Error("scheduling a Mealie poll", "err", err)
	}
	a.publish("updated", "shopping_list", m.list)
	switch {
	case errors.Is(err, errMealieGone):
		a.listsPage(w, r, http.StatusNotFound, errMealieRemoved)
	case err != nil:
		a.Log.Warn("Mealie write failed", "err", err)
		msg := []rune(err.Error())
		if _, err := a.DB.ExecContext(r.Context(), "UPDATE integrations SET consecutive_failures = max(consecutive_failures, ?), last_error = ? WHERE id = ?",
			mealieDownPoll, string(msg[:min(len(msg), 500)]), m.c.id); err != nil {
			a.serverError(w, r, err)
			return
		}
		a.listsPage(w, r, http.StatusServiceUnavailable, mealieUnreachable)
	default:
		http.Redirect(w, r, "/lists", http.StatusSeeOther)
	}
}

// pullMealieList re-pulls one Mirrored list and returns its items as Mealie has them.
func (a *App) pullMealieList(ctx context.Context, m *mirror) error {
	_, err := a.pullMealieItems(ctx, m)
	return err
}

func (a *App) pullMealieItems(ctx context.Context, m *mirror) ([]mealieItem, error) {
	var l mealieList
	err := m.c.call(ctx, http.MethodGet, mealieLists+"/"+url.PathEscape(m.ext), nil, &l)
	if errors.Is(err, errMealieGone) {
		_, err = a.DB.ExecContext(ctx, "DELETE FROM shopping_lists WHERE id = ?", m.list)
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return l.Items, a.inTx(ctx, func(tx *sql.Tx) error { return replaceMealieItems(ctx, tx, m.list, l.Items) })
}

// applyMealie stores what Mealie says a write changed; its merges are accepted as they come.
func (a *App) applyMealie(ctx context.Context, integration int64, col mealieCollection) error {
	return a.inTx(ctx, func(tx *sql.Tx) error {
		for _, it := range append(col.Created, col.Updated...) {
			var list int64
			err := tx.QueryRowContext(ctx, "SELECT id FROM shopping_lists WHERE integration_id = ? AND external_id = ?", integration, it.ShoppingListID).Scan(&list)
			if errors.Is(err, sql.ErrNoRows) {
				continue // a list the next poll brings in
			}
			if err != nil {
				return err
			}
			if err := upsertMealieItem(ctx, tx, list, it); err != nil {
				return err
			}
		}
		for _, it := range col.Deleted {
			if err := deleteMealieItem(ctx, tx, integration, it.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

func deleteMealieItem(ctx context.Context, tx *sql.Tx, integration int64, ext string) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM shopping_items WHERE external_id = ? AND list_id IN (SELECT id FROM shopping_lists WHERE integration_id = ?)", ext, integration)
	return err
}

// itemExt is item id's Mealie id on Mirrored list m.
func (a *App) itemExt(ctx context.Context, m *mirror, id int64) (string, error) {
	var ext string
	err := a.DB.QueryRowContext(ctx, "SELECT external_id FROM shopping_items WHERE id = ? AND list_id = ?", id, m.list).Scan(&ext)
	return ext, err
}

func (a *App) mealieAdd(w http.ResponseWriter, r *http.Request, m *mirror, note string) {
	a.mealieWrite(w, r, m, func(ctx context.Context) error {
		var pos int64
		if err := a.DB.QueryRowContext(ctx, "SELECT coalesce(max(position), -1) + 1 FROM shopping_items WHERE list_id = ?", m.list).Scan(&pos); err != nil {
			return err
		}
		var col mealieCollection
		if err := m.c.call(ctx, http.MethodPost, mealieItems, map[string]any{"shoppingListId": m.ext, "note": note, "position": pos}, &col); err != nil {
			return err
		}
		return a.applyMealie(ctx, m.c.id, col)
	})
}

// mealieCheck re-GETs the item, overlays only checked and PUTs the whole item
// back: Mealie's PUT is a full replace that wipes whatever isn't sent.
func (a *App) mealieCheck(w http.ResponseWriter, r *http.Request, m *mirror, id int64, checked bool) {
	ext, err := a.itemExt(r.Context(), m, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.mealieWrite(w, r, m, func(ctx context.Context) error {
		var raw json.RawMessage
		if err := m.c.call(ctx, http.MethodGet, mealieItems+"/"+url.PathEscape(ext), nil, &raw); err != nil {
			return err
		}
		d := json.NewDecoder(bytes.NewReader(raw))
		d.UseNumber() // numbers go back exactly as they came
		var item map[string]any
		if err := d.Decode(&item); err != nil {
			return transient(fmt.Errorf("Mealie's item isn't JSON: %w", err))
		}
		item["checked"] = checked
		var col mealieCollection
		if err := m.c.call(ctx, http.MethodPut, mealieItems+"/"+url.PathEscape(ext), item, &col); err != nil {
			return err
		}
		return a.applyMealie(ctx, m.c.id, col)
	})
}

func (a *App) mealieDelete(w http.ResponseWriter, r *http.Request, m *mirror, id int64) {
	ext, err := a.itemExt(r.Context(), m, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.mealieWrite(w, r, m, func(ctx context.Context) error {
		if err := m.c.call(ctx, http.MethodDelete, mealieItems+"/"+url.PathEscape(ext), nil, nil); err != nil && !errors.Is(err, errMealieGone) {
			return err // already gone is success
		}
		return a.inTx(ctx, func(tx *sql.Tx) error { return deleteMealieItem(ctx, tx, m.c.id, ext) })
	})
}

// mealieClear re-pulls the list and deletes only what is still checked in Mealie.
func (a *App) mealieClear(w http.ResponseWriter, r *http.Request, m *mirror) {
	a.mealieWrite(w, r, m, func(ctx context.Context) error {
		items, err := a.pullMealieItems(ctx, m)
		if err != nil {
			return err
		}
		q := url.Values{}
		for _, it := range items {
			if it.Checked {
				q.Add("ids", it.ID)
			}
		}
		if len(q) == 0 {
			return nil
		}
		if err := m.c.call(ctx, http.MethodDelete, mealieItems+"?"+q.Encode(), nil, nil); err != nil && !errors.Is(err, errMealieGone) {
			return err
		}
		return a.inTx(ctx, func(tx *sql.Tx) error {
			for _, ext := range q["ids"] {
				if err := deleteMealieItem(ctx, tx, m.c.id, ext); err != nil {
					return err
				}
			}
			return nil
		})
	})
}
