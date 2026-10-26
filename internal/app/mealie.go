package app

import (
	"bytes"
	"context"
	"database/sql"
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

// mealie mirrors every shopping list of one Mealie household (spec §10):
// Mealie first, no queue. Its credentials are the Bearer token, its
// config_json the instance's address.
type mealie struct{}

const (
	mealieType  = "mealie"
	mealiePoll  = 60 // seconds between full pulls
	mealieLists = "/api/households/shopping/lists"
	mealieItems = "/api/households/shopping/items"
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
	id   int64
	base string
	sc   syncClient
}

func newMealieConn(id int64, base, token string) mealieConn {
	return mealieConn{id: id, base: base, sc: syncClient{name: "Mealie", client: mealieClient,
		header: http.Header{"Authorization": {"Bearer " + token}, "Accept": {"application/json"}},
		limit:  10 << 20, overrides: map[int]error{http.StatusNotFound: errMealieGone}}}
}

// call sends body (if any) as JSON to Mealie and decodes its answer into out
// (if any). A 404 is errMealieGone.
func (c mealieConn) call(ctx context.Context, method, path string, body, out any) error {
	return c.sc.json(ctx, method, c.base+path, body, out)
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
	return truncateRunes(n, 200)
}

// truncateRunes is s cut to at most max characters (runes, not bytes).
func truncateRunes(s string, max int) string {
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
	changed := false // a poll that found nothing new publishes nothing
	return a.inTxIf(ctx, &changed, func(tx *sql.Tx) error {
		before, err := totalChanges(ctx, tx)
		if err != nil {
			return err
		}
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
		if _, err := tx.ExecContext(ctx, "DELETE FROM shopping_lists WHERE integration_id = ? AND external_id NOT IN ("+placeholders(len(keep)-1)+")", keep...); err != nil {
			return err
		}
		after, err := totalChanges(ctx, tx)
		changed = after != before
		return err
	}, web.KindList)
}

// totalChanges counts the rows tx's connection has changed: the upserts below
// leave a row that is already as Mealie has it alone, so it doesn't count.
func totalChanges(ctx context.Context, tx *sql.Tx) (n int64, err error) {
	err = tx.QueryRowContext(ctx, "SELECT total_changes()").Scan(&n)
	return n, err
}

// placeholders is n comma-separated ?s for an IN list; with none, an empty set.
func placeholders(n int) string {
	if n == 0 {
		return "SELECT NULL WHERE 0"
	}
	return strings.Repeat("?, ", n-1) + "?"
}

func upsertMealieList(ctx context.Context, tx *sql.Tx, integration int64, l mealieList) (int64, error) {
	name := truncateRunes(strings.TrimSpace(l.Name), 50)
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
	_, err = tx.ExecContext(ctx, "UPDATE shopping_lists SET name = ?1 WHERE id = ?2 AND name IS NOT ?1", name, id)
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
			note = excluded.note, checked = excluded.checked, position = excluded.position, extra_json = excluded.extra_json
		WHERE (note, checked, position, extra_json) IS NOT (excluded.note, excluded.checked, excluded.position, excluded.extra_json)`,
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
	return newMealieConn(id, cfg.URL, string(token)), nil
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
	msg, err := a.integrations.Connect(r.Context(), newIntegration{typ: mealieType, name: "Mealie", url: raw, secret: token,
		missingSecret: "Paste an API token from Mealie (Profile > API Tokens).", cfg: mealieConfig{URL: raw}, interval: mealiePoll, unique: true,
		check: func(ctx context.Context) string {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			var about struct {
				Version string `json:"version"`
			}
			if err := newMealieConn(0, raw, token).call(ctx, http.MethodGet, "/api/app/about", nil, &about); err != nil {
				return "Couldn't reach Mealie at that address."
			}
			if !mealieVersionOK(about.Version) {
				return fmt.Sprintf("GoCasa needs Mealie v2.0 or later; this one is %q.", about.Version)
			}
			return ""
		}})
	a.connected(w, r, msg, err)
}

// disconnectMealie removes Mealie and its Mirrored lists from GoCasa; Mealie keeps them.
func (a *App) disconnectMealie(w http.ResponseWriter, r *http.Request) {
	if _, err := a.exec(r.Context(), web.KindList, "DELETE FROM integrations WHERE type = ?", mealieType); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// hideMealieList shows or hides a Mirrored list.
func (a *App) hideMealieList(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	hidden := r.PostFormValue("hidden") == "on"
	if _, err := a.exec(r.Context(), web.KindList, "UPDATE shopping_lists SET hidden = ? WHERE id = ? AND integration_id IS NOT NULL", hidden, id); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

func (mealie) Section(ctx context.Context, a *App, st map[int64]integrationHealth, _ []web.Member) (templ.Component, error) {
	m, err := a.mealieSettings(ctx, st)
	return web.MealieSection(m), err
}

// mealieSettings is the Mealie section of Settings; nil when not connected.
func (a *App) mealieSettings(ctx context.Context, st map[int64]integrationHealth) (*web.Mealie, error) {
	var m web.Mealie
	var cfg string
	err := a.DB.QueryRowContext(ctx, `SELECT id, config_json FROM integrations WHERE type = ?`, mealieType).Scan(&m.ID, &cfg)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var c mealieConfig
	json.Unmarshal([]byte(cfg), &c)
	x := st[m.ID]
	m.URL, m.Failures, m.LastError, m.LastSuccess = c.URL, x.Failures, x.LastError, x.LastSuccess
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
	err := a.DB.QueryRowContext(ctx, `SELECT l.external_id, i.id, i.credentials_enc, `+downSQL("i")+`
		FROM shopping_lists l JOIN integrations i ON i.id = l.integration_id WHERE l.id = ?`, list).Scan(&m.ext, &m.c.id, &enc, &m.down)
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
	return m, nil
}

const mealieUnreachable = "GoCasa can't reach Mealie, so its lists show their last contents and can't be changed until it answers again."

// mealieWrite runs write against Mealie on Mirrored list m: a gone item
// re-pulls the list (ErrGoneFromMealie), any other failure makes the lists
// read-only until the next good poll (ErrReadOnly).
func (a *App) mealieWrite(ctx context.Context, m *mirror, write func(ctx context.Context) error) error {
	if m.down {
		return ErrReadOnly
	}
	var err, recErr error
	lockErr := a.integrations.WithLock(m.c.id, func() error { // not mid-poll
		// Again under the lock: a poll we waited behind may have found Mealie down.
		h, herr := a.integrations.Health(ctx, m.c.id)
		if m.down = h.Down; herr != nil || m.down {
			return herr
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		err = write(ctx)
		if errors.Is(err, errMealieGone) {
			if err = a.pullMealieList(ctx, m); err == nil {
				err = errMealieGone
			}
		}
		if err != nil && !errors.Is(err, errMealieGone) {
			// Recorded before the lock goes, so a write waiting on it sees Mealie down.
			a.Log.Warn("Mealie write failed", "err", err)
			recErr = a.integrations.RecordFailure(ctx, m.c.id, err)
		}
		return nil
	})
	switch {
	case lockErr != nil:
		return fmt.Errorf("Mealie write: %v", lockErr) // %v: a server error, never a missing row
	case m.down:
		return ErrReadOnly
	}
	// Poll soon after every write, on the next scheduler tick.
	if err := a.integrations.PollSoon(ctx, m.c.id); err != nil {
		a.Log.Error("scheduling a Mealie poll", "err", err)
	}
	switch {
	case errors.Is(err, errMealieGone):
		return ErrGoneFromMealie
	case recErr != nil:
		return fmt.Errorf("recording a Mealie failure: %v", recErr)
	case err != nil:
		return ErrReadOnly
	}
	return nil
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
		_, err = a.exec(ctx, web.KindList, "DELETE FROM shopping_lists WHERE id = ?", m.list)
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	return l.Items, a.inTx(ctx, func(tx *sql.Tx) error { return replaceMealieItems(ctx, tx, m.list, l.Items) }, web.KindList)
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
	}, web.KindList)
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

// mirroredList is a Shopping list mirrored from Mealie: every change goes to Mealie first.
type mirroredList struct {
	a *App
	m *mirror
}

func (l mirroredList) Add(ctx context.Context, note string) error {
	a, m := l.a, l.m
	return a.mealieWrite(ctx, m, func(ctx context.Context) error {
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

// Check re-GETs the item, overlays only checked and PUTs the whole item
// back: Mealie's PUT is a full replace that wipes whatever isn't sent.
func (l mirroredList) Check(ctx context.Context, id int64, checked bool) error {
	a, m := l.a, l.m
	ext, err := a.itemExt(ctx, m, id)
	if err != nil {
		return err
	}
	return a.mealieWrite(ctx, m, func(ctx context.Context) error {
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

func (l mirroredList) Delete(ctx context.Context, id int64) error {
	a, m := l.a, l.m
	ext, err := a.itemExt(ctx, m, id)
	if err != nil {
		return err
	}
	return a.mealieWrite(ctx, m, func(ctx context.Context) error {
		if err := m.c.call(ctx, http.MethodDelete, mealieItems+"/"+url.PathEscape(ext), nil, nil); err != nil && !errors.Is(err, errMealieGone) {
			return err // already gone is success
		}
		return a.inTx(ctx, func(tx *sql.Tx) error { return deleteMealieItem(ctx, tx, m.c.id, ext) }, web.KindList)
	})
}

// Clear re-pulls the list and deletes only what is still checked in Mealie.
func (l mirroredList) Clear(ctx context.Context) error {
	a, m := l.a, l.m
	return a.mealieWrite(ctx, m, func(ctx context.Context) error {
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
		}, web.KindList)
	})
}

// Move is refused as not found: a Mirrored list keeps Mealie's order.
func (mirroredList) Move(context.Context, int64, int) error { return sql.ErrNoRows }
