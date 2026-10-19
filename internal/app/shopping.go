package app

import (
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/DeRusto/goCasa/internal/web"
)

// Shopping is never locked (spec §9): no handler here checks a Lock or a PIN.

func (a *App) getLists(w http.ResponseWriter, r *http.Request) {
	// Opening the lists pulls Mirrored lists on the next scheduler tick; the page never waits on Mealie.
	// A live refresh isn't an open: a poll's own change would otherwise make the next poll due.
	if !background(r) {
		if _, err := a.DB.ExecContext(r.Context(), "UPDATE integrations SET next_sync_at = min(next_sync_at, ?) WHERE type = ?", a.Now().Unix(), mealieType); err != nil {
			a.serverError(w, r, err)
			return
		}
	}
	a.listsPage(w, r, http.StatusOK, "")
}

func (a *App) listsPage(w http.ResponseWriter, r *http.Request, status int, msg string) {
	rows, err := a.DB.QueryContext(r.Context(), `SELECT l.id, l.name, l.integration_id IS NOT NULL, coalesce(g.consecutive_failures >= ?, 0), i.id, i.note, i.checked
		FROM shopping_lists l LEFT JOIN shopping_items i ON i.list_id = l.id LEFT JOIN integrations g ON g.id = l.integration_id
		WHERE NOT l.hidden ORDER BY l.position, l.id, i.position, i.id`, mealieDownPoll)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer rows.Close()
	var lists []web.ShoppingList
	for rows.Next() {
		var l web.ShoppingList
		var itemID sql.NullInt64
		var note sql.NullString
		var checked sql.NullBool
		if err := rows.Scan(&l.ID, &l.Name, &l.Mirrored, &l.ReadOnly, &itemID, &note, &checked); err != nil {
			a.serverError(w, r, err)
			return
		}
		if n := len(lists); n == 0 || lists[n-1].ID != l.ID {
			lists = append(lists, l)
		}
		if itemID.Valid {
			last := &lists[len(lists)-1]
			last.Items = append(last.Items, web.ShoppingItem{ID: itemID.Int64, Note: note.String, Checked: checked.Bool})
		}
	}
	if err := rows.Err(); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, status, web.Lists(lists, msg))
}

// mirrored hands a request on a Mirrored list to Mealie: it reports whether
// list is one (or the lookup failed and was answered).
func (a *App) mirrored(w http.ResponseWriter, r *http.Request, list int64, write func(*mirror)) bool {
	m, err := a.mirrorOf(r.Context(), list)
	if err != nil {
		a.serverError(w, r, err)
		return true
	}
	if m == nil {
		return false
	}
	write(m)
	return true
}

// text is form field key trimmed, or "" when it is longer than max characters.
func text(r *http.Request, key string, max int) string {
	v := strings.TrimSpace(r.PostFormValue(key))
	if len([]rune(v)) > max {
		return ""
	}
	return v
}

func (a *App) createList(w http.ResponseWriter, r *http.Request) {
	name := text(r, "name", 50)
	if name == "" {
		http.Error(w, "A list needs a name of 1 to 50 characters.", http.StatusBadRequest)
		return
	}
	var id int64
	if err := a.DB.QueryRowContext(r.Context(), `INSERT INTO shopping_lists (name, position)
		VALUES (?, (SELECT coalesce(max(position), -1) + 1 FROM shopping_lists)) RETURNING id`, name).Scan(&id); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.publish("created", "shopping_list", id)
	http.Redirect(w, r, "/lists", http.StatusSeeOther)
}

func (a *App) addItem(w http.ResponseWriter, r *http.Request) {
	list, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	note := text(r, "note", 200)
	if note == "" {
		http.Error(w, "An item needs 1 to 200 characters.", http.StatusBadRequest)
		return
	}
	if a.mirrored(w, r, list, func(m *mirror) { a.mealieAdd(w, r, m, note) }) {
		return
	}
	var id int64
	err := a.DB.QueryRowContext(r.Context(), `INSERT INTO shopping_items (list_id, note, position)
		SELECT id, ?, (SELECT coalesce(max(position), -1) + 1 FROM shopping_items WHERE list_id = ?)
		FROM shopping_lists WHERE id = ? AND integration_id IS NULL RETURNING id`, note, list, list).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
		return
	case err != nil:
		a.serverError(w, r, err)
		return
	}
	a.publish("created", "shopping_item", id)
	http.Redirect(w, r, "/lists", http.StatusSeeOther)
}

var errBadPosition = errors.New("bad position")

// moveItem puts an item at a 0-based position in its list and renumbers the list.
func (a *App) moveItem(w http.ResponseWriter, r *http.Request) {
	id, list := itemPath(r)
	to, err := strconv.Atoi(r.PostFormValue("position"))
	if err != nil {
		to = -1
	}
	err = a.inTx(r.Context(), func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(r.Context(), "SELECT EXISTS (SELECT 1 FROM shopping_items WHERE "+localItem+")", id, list).Scan(&exists); err != nil {
			return err
		} else if !exists {
			return sql.ErrNoRows
		}
		rows, err := tx.QueryContext(r.Context(), "SELECT id FROM shopping_items WHERE list_id = ? AND id != ? ORDER BY position, id", list, id)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var i int64
			if err := rows.Scan(&i); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, i)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if to < 0 || to > len(ids) {
			return errBadPosition
		}
		ids = slices.Insert(ids, to, id)
		for pos, i := range ids {
			if _, err := tx.ExecContext(r.Context(), "UPDATE shopping_items SET position = ? WHERE id = ?", pos, i); err != nil {
				return err
			}
		}
		return nil
	})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	case errors.Is(err, errBadPosition):
		http.Error(w, "That position is outside the list.", http.StatusBadRequest)
	case err != nil:
		a.serverError(w, r, err)
	default:
		a.publish("updated", "shopping_list", list)
		http.Redirect(w, r, "/lists", http.StatusSeeOther)
	}
}

// localItem limits a query on shopping_items to item ? on local list ?; a Mirrored list changes through Mealie.
const localItem = "id = ? AND list_id = (SELECT id FROM shopping_lists WHERE id = ? AND integration_id IS NULL)"

// itemPath is the item and list ids of a /lists/{list}/items/{id} request.
func itemPath(r *http.Request) (item, list int64) {
	item, _ = strconv.ParseInt(r.PathValue("id"), 10, 64)
	list, _ = strconv.ParseInt(r.PathValue("list"), 10, 64)
	return item, list
}

func (a *App) checkItem(w http.ResponseWriter, r *http.Request) {
	id, list := itemPath(r)
	checked, err := strconv.ParseBool(r.PostFormValue("checked"))
	if err != nil {
		http.Error(w, "checked must be true or false.", http.StatusBadRequest)
		return
	}
	if a.mirrored(w, r, list, func(m *mirror) { a.mealieCheck(w, r, m, id, checked) }) {
		return
	}
	a.localChange(w, r, "updated", id, "UPDATE shopping_items SET checked = ? WHERE "+localItem, checked, id, list)
}

func (a *App) deleteItem(w http.ResponseWriter, r *http.Request) {
	id, list := itemPath(r)
	if a.mirrored(w, r, list, func(m *mirror) { a.mealieDelete(w, r, m, id) }) {
		return
	}
	a.localChange(w, r, "deleted", id, "DELETE FROM shopping_items WHERE "+localItem, id, list)
}

// clearChecked deletes a list's checked items.
func (a *App) clearChecked(w http.ResponseWriter, r *http.Request) {
	list, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if a.mirrored(w, r, list, func(m *mirror) { a.mealieClear(w, r, m) }) {
		return
	}
	var exists bool
	if err := a.DB.QueryRowContext(r.Context(), "SELECT EXISTS (SELECT 1 FROM shopping_lists WHERE id = ?)", list).Scan(&exists); err != nil {
		a.serverError(w, r, err)
		return
	}
	if !exists {
		http.NotFound(w, r)
		return
	}
	if _, err := a.DB.ExecContext(r.Context(), "DELETE FROM shopping_items WHERE checked AND list_id = ?", list); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.publish("updated", "shopping_list", list)
	http.Redirect(w, r, "/lists", http.StatusSeeOther)
}

// localChange runs query on one item of a local list; no row changed is a 404.
func (a *App) localChange(w http.ResponseWriter, r *http.Request, typ string, id int64, query string, args ...any) {
	res, err := a.DB.ExecContext(r.Context(), query, args...)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.NotFound(w, r)
		return
	}
	a.publish(typ, "shopping_item", id)
	http.Redirect(w, r, "/lists", http.StatusSeeOther)
}
