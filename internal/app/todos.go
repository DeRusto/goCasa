package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
)

// getTodos shows the board, filtered by status (open, done, all) and a due
// range (a preset, or from/to dates that override it), in manual or due order.
func (a *App) getTodos(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	today := dayAt(a.Now().In(a.Config.Location), 0)
	p := web.TodosPage{Status: q.Get("status"), Due: q.Get("due"), Sort: q.Get("sort")}
	where, args := []string{"1"}, []any{}
	switch p.Status {
	case "done":
		where = append(where, "completed_at IS NOT NULL")
	case "all":
	default:
		p.Status = "open"
		where = append(where, "completed_at IS NULL")
	}
	var from, to string
	switch p.Due {
	case "overdue":
		to = dayAt(today, -1).Format(dateLayout)
	case "today":
		to = today.Format(dateLayout)
	case "week":
		to = dayAt(today, 6).Format(dateLayout)
	case "none":
		where = append(where, "due_date IS NULL")
	default:
		p.Due = ""
	}
	if _, err := time.Parse(dateLayout, q.Get("from")); err == nil {
		p.From, from = q.Get("from"), q.Get("from")
	}
	if _, err := time.Parse(dateLayout, q.Get("to")); err == nil {
		p.To, to = q.Get("to"), q.Get("to")
	}
	if from != "" {
		where, args = append(where, "due_date >= ?"), append(args, from)
	}
	if to != "" {
		where, args = append(where, "due_date <= ?"), append(args, to)
	}
	order := "position, id"
	if p.Sort == "due" {
		order = "due_date IS NULL, due_date, " + order
	} else {
		p.Sort = ""
	}
	p.Draggable = p.Status == "open" && p.Due == "" && from == "" && to == "" && p.Sort == ""

	cols, err := a.todoColumns(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	at := map[int64]int{}
	for i, c := range cols {
		at[c.ID] = i
	}
	rows, err := a.DB.QueryContext(r.Context(), "SELECT id, column_id, title, notes, coalesce(due_date, ''), completed_at IS NOT NULL FROM todos WHERE "+strings.Join(where, " AND ")+" ORDER BY "+order, args...)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var t web.Todo
		if err := rows.Scan(&t.ID, &t.ColumnID, &t.Title, &t.Notes, &t.Due, &t.Done); err != nil {
			a.serverError(w, r, err)
			return
		}
		t.Overdue = !t.Done && t.Due != "" && t.Due < today.Format(dateLayout)
		cols[at[t.ColumnID]].Todos = append(cols[at[t.ColumnID]].Todos, t)
	}
	if err := rows.Err(); err != nil {
		a.serverError(w, r, err)
		return
	}
	p.Columns = cols
	a.render(w, r, http.StatusOK, web.TodosBoard(p))
}

func (a *App) todoColumns(ctx context.Context) ([]web.TodoColumn, error) {
	rows, err := a.DB.QueryContext(ctx, "SELECT id, name FROM todo_columns ORDER BY position, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []web.TodoColumn
	for rows.Next() {
		var c web.TodoColumn
		if err := rows.Scan(&c.ID, &c.Name); err != nil {
			return nil, err
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

func columnName(r *http.Request) (string, bool) {
	name := strings.TrimSpace(r.PostFormValue("name"))
	return name, name != "" && len([]rune(name)) <= 50
}

func (a *App) createTodoColumn(w http.ResponseWriter, r *http.Request) {
	name, ok := columnName(r)
	if !ok {
		http.Error(w, "A column needs a name of 1 to 50 characters.", http.StatusBadRequest)
		return
	}
	var id int64
	if err := a.DB.QueryRowContext(r.Context(), "INSERT INTO todo_columns (name, position) VALUES (?, (SELECT coalesce(max(position) + 1, 0) FROM todo_columns)) RETURNING id", name).Scan(&id); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.publish("created", "todo_column", id)
	http.Redirect(w, r, "/todos", http.StatusSeeOther)
}

func (a *App) renameTodoColumn(w http.ResponseWriter, r *http.Request) {
	name, ok := columnName(r)
	if !ok {
		http.Error(w, "A column needs a name of 1 to 50 characters.", http.StatusBadRequest)
		return
	}
	a.todoExec(w, r, "updated", "todo_column", "UPDATE todo_columns SET name = ? WHERE id = ?", name)
}

func (a *App) deleteTodoColumn(w http.ResponseWriter, r *http.Request) {
	a.todoExec(w, r, "deleted", "todo_column", "DELETE FROM todo_columns WHERE id = ?")
}

func (a *App) completeTodo(w http.ResponseWriter, r *http.Request) {
	a.todoExec(w, r, "updated", "todo", "UPDATE todos SET completed_at = ? WHERE id = ?", a.Now().Unix())
}

func (a *App) reopenTodo(w http.ResponseWriter, r *http.Request) {
	a.todoExec(w, r, "updated", "todo", "UPDATE todos SET completed_at = NULL WHERE id = ?")
}

func (a *App) deleteTodo(w http.ResponseWriter, r *http.Request) {
	a.todoExec(w, r, "deleted", "todo", "DELETE FROM todos WHERE id = ?")
}

// todoExec runs query with args and the path's id last: 404 if no row changed,
// else it publishes the change and returns to the board.
func (a *App) todoExec(w http.ResponseWriter, r *http.Request, typ, entity, query string, args ...any) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	res, err := a.DB.ExecContext(r.Context(), query, append(args, id)...)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.NotFound(w, r)
		return
	}
	a.publish(typ, entity, id)
	http.Redirect(w, r, "/todos", http.StatusSeeOther)
}

// todoFields reads and checks the to-do form, or returns a message for the person.
func todoFields(r *http.Request) (t web.Todo, msg string) {
	t.ColumnID, _ = strconv.ParseInt(r.PostFormValue("column_id"), 10, 64)
	t.Title = strings.TrimSpace(r.PostFormValue("title"))
	t.Notes = strings.TrimSpace(r.PostFormValue("notes"))
	t.Due = r.PostFormValue("due_date")
	switch {
	case t.Title == "" || len([]rune(t.Title)) > 200:
		return t, "A to-do needs a title of 1 to 200 characters."
	case len([]rune(t.Notes)) > 5000:
		return t, "Notes are at most 5000 characters."
	}
	if _, err := time.Parse(dateLayout, t.Due); t.Due != "" && err != nil {
		return t, "Pick a due date, or leave it empty."
	}
	return t, ""
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// appendPosition is the place after the last to-do in column ?.
const appendPosition = "(SELECT coalesce(max(position) + 1, 0) FROM todos WHERE column_id = ?)"

func (a *App) createTodo(w http.ResponseWriter, r *http.Request) {
	t, msg := todoFields(r)
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	var id int64
	err := a.DB.QueryRowContext(r.Context(), "INSERT INTO todos (column_id, title, notes, due_date, position, created_at) SELECT id, ?, ?, ?, "+appendPosition+", ? FROM todo_columns WHERE id = ? RETURNING id",
		t.Title, t.Notes, nullable(t.Due), t.ColumnID, a.Now().Unix(), t.ColumnID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Pick a column.", http.StatusBadRequest)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.publish("created", "todo", id)
	http.Redirect(w, r, "/todos", http.StatusSeeOther)
}

func (a *App) getTodo(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	t := web.Todo{ID: id}
	err := a.DB.QueryRowContext(r.Context(), "SELECT column_id, title, notes, coalesce(due_date, ''), completed_at IS NOT NULL FROM todos WHERE id = ?", id).
		Scan(&t.ColumnID, &t.Title, &t.Notes, &t.Due, &t.Done)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	cols, err := a.todoColumns(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.render(w, r, http.StatusOK, web.TodoEditPage(t, cols))
}

// updateTodo saves the form; a to-do moved to another column goes last there.
func (a *App) updateTodo(w http.ResponseWriter, r *http.Request) {
	t, msg := todoFields(r)
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	var exists bool
	if err := a.DB.QueryRowContext(r.Context(), "SELECT EXISTS (SELECT 1 FROM todo_columns WHERE id = ?)", t.ColumnID).Scan(&exists); err != nil {
		a.serverError(w, r, err)
		return
	} else if !exists {
		http.Error(w, "Pick a column.", http.StatusBadRequest)
		return
	}
	a.todoExec(w, r, "updated", "todo", `UPDATE todos SET title = ?1, notes = ?2, due_date = ?3,
		position = CASE WHEN column_id = ?4 THEN position ELSE (SELECT coalesce(max(position) + 1, 0) FROM todos WHERE column_id = ?4) END,
		column_id = ?4 WHERE id = ?5`,
		t.Title, t.Notes, nullable(t.Due), t.ColumnID)
}

// reorderTodos sets column_id's order to ids, moving in any to-do dragged from
// another column. To-dos the board didn't show (completed ones) keep their
// old positions; ties fall back to id.
func (a *App) reorderTodos(w http.ResponseWriter, r *http.Request) {
	col, _ := strconv.ParseInt(r.PostFormValue("column_id"), 10, 64)
	ids := parseIDs(r.PostForm["ids"])
	tx, err := a.DB.BeginTx(r.Context(), nil)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(r.Context(), "SELECT EXISTS (SELECT 1 FROM todo_columns WHERE id = ?)", col).Scan(&exists); err != nil {
		a.serverError(w, r, err)
		return
	} else if !exists {
		http.Error(w, "No such column.", http.StatusBadRequest)
		return
	}
	for i, id := range ids {
		res, err := tx.ExecContext(r.Context(), "UPDATE todos SET column_id = ?, position = ? WHERE id = ?", col, i, id)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			http.Error(w, "No such to-do.", http.StatusBadRequest)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.publish("updated", "todo_column", col)
	w.WriteHeader(http.StatusOK)
}
