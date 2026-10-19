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
	"github.com/teambition/rrule-go"
)

// getTodos shows the board, filtered by status (open, done, all) and a due
// range (a preset, or from/to dates that override it), in manual or due order.
func (a *App) getTodos(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	today := dayAt(a.Now().In(a.location(r.Context())), 0)
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

// completeTodo completes an open to-do; a Recurring to-do's completion also
// creates its next copy, cloned from this one. Completing a completed to-do
// changes nothing, so the next copy is created exactly once.
func (a *App) completeTodo(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	now := a.Now()
	tx, err := a.DB.BeginTx(r.Context(), nil)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	var rule sql.NullString
	var fromCompletion bool
	var due string
	err = tx.QueryRowContext(r.Context(), "UPDATE todos SET completed_at = ? WHERE id = ? AND completed_at IS NULL RETURNING repeat_rule, repeat_from_completion, coalesce(due_date, '')", now.Unix(), id).
		Scan(&rule, &fromCompletion, &due)
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		if err := tx.QueryRowContext(r.Context(), "SELECT EXISTS (SELECT 1 FROM todos WHERE id = ?)", id).Scan(&exists); err != nil {
			a.serverError(w, r, err)
		} else if !exists {
			http.NotFound(w, r)
		} else {
			http.Redirect(w, r, "/todos", http.StatusSeeOther)
		}
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	var next int64
	if rule.Valid {
		nextDue, err := nextTodoDue(rule.String, fromCompletion, due, dayAt(now.In(a.location(r.Context())), 0))
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		if err := tx.QueryRowContext(r.Context(), `INSERT INTO todos (column_id, title, notes, due_date, position, created_at, repeat_rule, repeat_from_completion)
			SELECT column_id, title, notes, ?, position, ?, repeat_rule, repeat_from_completion FROM todos WHERE id = ? RETURNING id`, nextDue, now.Unix(), id).Scan(&next); err != nil {
			a.serverError(w, r, err)
			return
		}
		if _, err := tx.ExecContext(r.Context(), "UPDATE todos SET next_id = ? WHERE id = ?", next, id); err != nil {
			a.serverError(w, r, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		a.serverError(w, r, err)
		return
	}
	a.publish("updated", "todo", id)
	if next != 0 {
		a.publish("created", "todo", next)
	}
	http.Redirect(w, r, "/todos", http.StatusSeeOther)
}

// nextTodoDue is the due date of a Recurring to-do's next copy, given the
// completed copy's due date and the day it was completed. Repeat from
// schedule takes the first rule date after both, so missed dates are skipped
// and early completion doesn't use up the next one; Repeat from completion
// counts one interval from the completion day.
func nextTodoDue(rule string, fromCompletion bool, due string, today time.Time) (string, error) {
	opt, err := rrule.StrToROption(rule)
	if err != nil {
		return "", err
	}
	day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	if fromCompletion {
		n := opt.Interval
		switch opt.Freq {
		case rrule.YEARLY:
			day = day.AddDate(n, 0, 0)
		case rrule.MONTHLY:
			day = day.AddDate(0, n, 0)
		case rrule.WEEKLY:
			day = day.AddDate(0, 0, 7*n)
		default:
			day = day.AddDate(0, 0, n)
		}
		return day.Format(dateLayout), nil
	}
	start, err := time.Parse(dateLayout, due)
	if err != nil {
		return "", err
	}
	if day.Before(start) {
		day = start
	}
	return ruleDate(opt, start, day, false)
}

// ruleDate is the first date of rule, counted from start, after day (or on it, when inc).
func ruleDate(opt *rrule.ROption, start, day time.Time, inc bool) (string, error) {
	opt.Dtstart = start
	rr, err := rrule.NewRRule(*opt)
	if err != nil {
		return "", err
	}
	d := rr.After(day, inc)
	if d.IsZero() {
		return "", errors.New("repeat rule has no later date")
	}
	return d.Format(dateLayout), nil
}

// reopenTodo un-completes a to-do. If its completion created a next copy that
// is still open, that copy is deleted; a completed next copy must be
// re-opened first.
func (a *App) reopenTodo(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	tx, err := a.DB.BeginTx(r.Context(), nil)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	defer tx.Rollback()
	var next sql.NullInt64
	err = tx.QueryRowContext(r.Context(), "SELECT next_id FROM todos WHERE id = ?", id).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	} else if err != nil {
		a.serverError(w, r, err)
		return
	}
	if next.Valid {
		res, err := tx.ExecContext(r.Context(), "DELETE FROM todos WHERE id = ? AND completed_at IS NULL", next.Int64)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			http.Error(w, "The next copy of this to-do is already completed: re-open the later one first.", http.StatusConflict)
			return
		}
	}
	if _, err := tx.ExecContext(r.Context(), "UPDATE todos SET completed_at = NULL, next_id = NULL WHERE id = ?", id); err != nil {
		a.serverError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		a.serverError(w, r, err)
		return
	}
	if next.Valid {
		a.publish("deleted", "todo", next.Int64)
	}
	a.publish("updated", "todo", id)
	http.Redirect(w, r, "/todos", http.StatusSeeOther)
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

// todoFields reads and checks the to-do form, or returns a message for the
// person. rule is its preset repeat rule, "" when it doesn't repeat; a
// Recurring to-do without a due date gets the rule's first date from today.
func todoFields(r *http.Request, today time.Time) (t web.Todo, rule, msg string) {
	t, msg = todoText(r)
	if msg != "" {
		return t, "", msg
	}
	t.Repeat, t.Interval, t.ByDay = r.PostFormValue("repeat"), r.PostFormValue("interval"), map[string]bool{}
	t.FromCompletion = r.PostFormValue("repeat_from") == "completion"
	switch t.Repeat {
	case "":
		return t, "", ""
	case "daily", "weekly", "monthly", "yearly":
	default:
		return t, "", "Pick how it repeats."
	}
	if rule, msg = presetRule(r, t.ByDay); msg != "" || t.Due != "" {
		return t, rule, msg
	}
	opt, err := rrule.StrToROption(rule)
	if err == nil {
		day := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
		t.Due, err = ruleDate(opt, day, day, true)
	}
	if err != nil {
		return t, "", "That repeat rule isn't one GoCasa understands."
	}
	return t, rule, ""
}

// todoText reads and checks the to-do form's column, title, notes and due date.
func todoText(r *http.Request) (t web.Todo, msg string) {
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
	t, rule, msg := todoFields(r, a.Now().In(a.location(r.Context())))
	if msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	var id int64
	err := a.DB.QueryRowContext(r.Context(), "INSERT INTO todos (column_id, title, notes, due_date, position, created_at, repeat_rule, repeat_from_completion) SELECT id, ?, ?, ?, "+appendPosition+", ?, ?, ? FROM todo_columns WHERE id = ? RETURNING id",
		t.Title, t.Notes, nullable(t.Due), t.ColumnID, a.Now().Unix(), nullable(rule), t.FromCompletion, t.ColumnID).Scan(&id)
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
	t := web.Todo{ID: id, ByDay: map[string]bool{}}
	var rule string
	err := a.DB.QueryRowContext(r.Context(), "SELECT column_id, title, notes, coalesce(due_date, ''), completed_at IS NOT NULL, coalesce(repeat_rule, ''), repeat_from_completion FROM todos WHERE id = ?", id).
		Scan(&t.ColumnID, &t.Title, &t.Notes, &t.Due, &t.Done, &rule, &t.FromCompletion)
	for _, p := range strings.Split(rule, ";") { // a preset: FREQ, INTERVAL and BYDAY only
		k, v, _ := strings.Cut(p, "=")
		switch k {
		case "FREQ":
			t.Repeat = strings.ToLower(v)
		case "INTERVAL":
			t.Interval = v
		case "BYDAY":
			for _, d := range strings.Split(v, ",") {
				t.ByDay[d] = true
			}
		}
	}
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
	t, rule, msg := todoFields(r, a.Now().In(a.location(r.Context())))
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
	// Completed copies are never touched (spec §8).
	var done bool
	if err := a.DB.QueryRowContext(r.Context(), "SELECT completed_at IS NOT NULL FROM todos WHERE id = ?", r.PathValue("id")).Scan(&done); err != nil && !errors.Is(err, sql.ErrNoRows) {
		a.serverError(w, r, err)
		return
	} else if done {
		http.Error(w, "A completed to-do can't be edited; reopen it first.", http.StatusConflict)
		return
	}
	a.todoExec(w, r, "updated", "todo", `UPDATE todos SET title = ?1, notes = ?2, due_date = ?3,
		position = CASE WHEN column_id = ?4 THEN position ELSE (SELECT coalesce(max(position) + 1, 0) FROM todos WHERE column_id = ?4) END,
		column_id = ?4, repeat_rule = ?5, repeat_from_completion = ?6 WHERE id = ?7`,
		t.Title, t.Notes, nullable(t.Due), t.ColumnID, nullable(rule), t.FromCompletion)
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
