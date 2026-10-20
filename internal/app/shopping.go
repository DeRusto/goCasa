package app

import (
	"context"
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
		var id int64
		err := a.DB.QueryRowContext(r.Context(), "SELECT id FROM integrations WHERE type = ?", mealieType).Scan(&id)
		if err == nil {
			err = a.integrations.PollSoon(r.Context(), id)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			a.serverError(w, r, err)
			return
		}
	}
	a.listsPage(w, r, http.StatusOK, "")
}

func (a *App) listsPage(w http.ResponseWriter, r *http.Request, status int, msg string) {
	rows, err := a.DB.QueryContext(r.Context(), `SELECT l.id, l.name, l.integration_id IS NOT NULL, coalesce(`+downSQL("g")+`, 0), i.id, i.note, i.checked
		FROM shopping_lists l LEFT JOIN shopping_items i ON i.list_id = l.id LEFT JOIN integrations g ON g.id = l.integration_id
		WHERE NOT l.hidden ORDER BY l.position, l.id, i.position, i.id`)
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
	if _, err := a.exec(r.Context(), web.KindList, `INSERT INTO shopping_lists (name, position)
		VALUES (?, (SELECT coalesce(max(position), -1) + 1 FROM shopping_lists))`, name); err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/lists", http.StatusSeeOther)
}

func (a *App) addItem(w http.ResponseWriter, r *http.Request) {
	list, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	note := text(r, "note", 200)
	if note == "" {
		http.Error(w, "An item needs 1 to 200 characters.", http.StatusBadRequest)
		return
	}
	if l := a.listOf(w, r, list); l != nil {
		a.listDone(w, r, l.Add(r.Context(), note))
	}
}

// moveItem puts an item at a 0-based position in its list.
func (a *App) moveItem(w http.ResponseWriter, r *http.Request) {
	id, list := itemPath(r)
	to, err := strconv.Atoi(r.PostFormValue("position"))
	if err != nil {
		to = -1
	}
	if l := a.listOf(w, r, list); l != nil {
		a.listDone(w, r, l.Move(r.Context(), id, to))
	}
}

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
	if l := a.listOf(w, r, list); l != nil {
		a.listDone(w, r, l.Check(r.Context(), id, checked))
	}
}

func (a *App) deleteItem(w http.ResponseWriter, r *http.Request) {
	id, list := itemPath(r)
	if l := a.listOf(w, r, list); l != nil {
		a.listDone(w, r, l.Delete(r.Context(), id))
	}
}

// clearChecked deletes a list's checked items.
func (a *App) clearChecked(w http.ResponseWriter, r *http.Request) {
	list, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if l := a.listOf(w, r, list); l != nil {
		a.listDone(w, r, l.Clear(r.Context()))
	}
}

// listOf is Shopping list id, or nil once a failed lookup is answered.
func (a *App) listOf(w http.ResponseWriter, r *http.Request, id int64) shoppingList {
	l, err := a.shoppingList(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return nil
	}
	return l
}

// listDone answers a change to a Shopping list.
func (a *App) listDone(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case err == nil:
		http.Redirect(w, r, "/lists", http.StatusSeeOther)
	case errors.Is(err, ErrReadOnly):
		a.listsPage(w, r, http.StatusServiceUnavailable, mealieUnreachable)
	case errors.Is(err, ErrGoneFromMealie):
		a.listsPage(w, r, http.StatusNotFound, errMealieRemoved)
	case errors.Is(err, sql.ErrNoRows):
		http.NotFound(w, r)
	case errors.Is(err, errBadPosition):
		http.Error(w, "That position is outside the list.", http.StatusBadRequest)
	default:
		a.serverError(w, r, err)
	}
}

// shoppingList is one Shopping list, Local or Mirrored. A missing list or
// item is sql.ErrNoRows.
type shoppingList interface {
	Add(ctx context.Context, note string) error
	Check(ctx context.Context, item int64, checked bool) error
	Delete(ctx context.Context, item int64) error
	// Clear deletes the checked items.
	Clear(ctx context.Context) error
	// Move puts an item at a 0-based position; only a Local list moves.
	Move(ctx context.Context, item int64, to int) error
}

var (
	// ErrReadOnly is a change to a Mirrored list while Mealie is Down or failing.
	ErrReadOnly = errors.New("Mealie can't be reached: the list is read-only")
	// ErrGoneFromMealie is a change to an item removed in Mealie; the list was re-pulled.
	ErrGoneFromMealie = errors.New("the item was removed in Mealie")
	errBadPosition    = errors.New("bad position")
)

// shoppingList is list id; a missing list is a Local list with no rows.
func (a *App) shoppingList(ctx context.Context, id int64) (shoppingList, error) {
	m, err := a.mirrorOf(ctx, id)
	if err != nil {
		return nil, err
	}
	if m != nil {
		return mirroredList{a, m}, nil
	}
	return localList{a, id}, nil
}

// localList is a Shopping list kept only in GoCasa.
type localList struct {
	a  *App
	id int64
}

func (l localList) Add(ctx context.Context, note string) error {
	return l.change(ctx, `INSERT INTO shopping_items (list_id, note, position)
		SELECT id, ?, (SELECT coalesce(max(position), -1) + 1 FROM shopping_items WHERE list_id = ?)
		FROM shopping_lists WHERE id = ? AND integration_id IS NULL`, note, l.id, l.id)
}

func (l localList) Check(ctx context.Context, item int64, checked bool) error {
	return l.change(ctx, "UPDATE shopping_items SET checked = ? WHERE "+localItem, checked, item, l.id)
}

func (l localList) Delete(ctx context.Context, item int64) error {
	return l.change(ctx, "DELETE FROM shopping_items WHERE "+localItem, item, l.id)
}

func (l localList) Clear(ctx context.Context) error {
	var exists bool
	if err := l.a.DB.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM shopping_lists WHERE id = ?)", l.id).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return sql.ErrNoRows
	}
	_, err := l.a.exec(ctx, web.KindList, "DELETE FROM shopping_items WHERE checked AND list_id = ?", l.id)
	return err
}

// Move renumbers the list with item at position to.
func (l localList) Move(ctx context.Context, item int64, to int) error {
	return l.a.inTx(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM shopping_items WHERE "+localItem+")", item, l.id).Scan(&exists); err != nil {
			return err
		} else if !exists {
			return sql.ErrNoRows
		}
		rows, err := tx.QueryContext(ctx, "SELECT id FROM shopping_items WHERE list_id = ? AND id != ? ORDER BY position, id", l.id, item)
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
		ids = slices.Insert(ids, to, item)
		for pos, i := range ids {
			if _, err := tx.ExecContext(ctx, "UPDATE shopping_items SET position = ? WHERE id = ?", pos, i); err != nil {
				return err
			}
		}
		return nil
	}, web.KindList)
}

// localItem limits a query on shopping_items to item ? on local list ?; a Mirrored list changes through Mealie.
const localItem = "id = ? AND list_id = (SELECT id FROM shopping_lists WHERE id = ? AND integration_id IS NULL)"

// change runs query on one item of the list; no row changed is sql.ErrNoRows.
func (l localList) change(ctx context.Context, query string, args ...any) error {
	return l.a.inTx(ctx, func(tx *sql.Tx) error { return execOne(ctx, tx, query, args...) }, web.KindList)
}
