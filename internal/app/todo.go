package app

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
	"github.com/teambition/rrule-go"
)

// todos is the to-do module: the board's columns and to-dos, and the
// Recurring to-do chain. Its invariant: only one copy of a Recurring to-do is
// open at a time. Each write runs in one transaction and returns one of the
// refusals below rather than break it.
type todos struct{ *App }

var (
	errTodoGone      = errors.New("no such to-do")
	errNoColumn      = errors.New("no such column")
	errTodoCompleted = errors.New("the to-do is completed")
	errTodoChained   = errors.New("the to-do's next copy still exists")
	errNextCompleted = errors.New("the to-do's next copy is completed")
)

// todoFilter picks to-dos by status and due range; the board and Today share it.
type todoFilter struct {
	Status   string // "open", "done", or anything else for all
	From, To string // due-date bounds (YYYY-MM-DD), "" for none
	NoDue    bool   // only to-dos without a due date
	ByDue    bool   // ordered by due date first, else by position
}

// List is the to-dos f picks, marking open ones due before today as overdue.
func (m todos) List(ctx context.Context, f todoFilter, today string) ([]web.Todo, error) {
	where, args := []string{"1"}, []any{}
	switch f.Status {
	case "done":
		where = append(where, "completed_at IS NOT NULL")
	case "open":
		where = append(where, "completed_at IS NULL")
	}
	if f.NoDue {
		where = append(where, "due_date IS NULL")
	}
	if f.From != "" {
		where, args = append(where, "due_date >= ?"), append(args, f.From)
	}
	if f.To != "" {
		where, args = append(where, "due_date <= ?"), append(args, f.To)
	}
	order := "position, id"
	if f.ByDue {
		order = "due_date IS NULL, due_date, " + order
	}
	rows, err := m.DB.QueryContext(ctx, "SELECT id, column_id, title, notes, coalesce(due_date, ''), completed_at IS NOT NULL FROM todos WHERE "+strings.Join(where, " AND ")+" ORDER BY "+order, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ts []web.Todo
	for rows.Next() {
		var t web.Todo
		if err := rows.Scan(&t.ID, &t.ColumnID, &t.Title, &t.Notes, &t.Due, &t.Done); err != nil {
			return nil, err
		}
		t.Overdue = !t.Done && t.Due != "" && t.Due < today
		ts = append(ts, t)
	}
	return ts, rows.Err()
}

// Get is to-do id with its repeat decoded for the form.
func (m todos) Get(ctx context.Context, id int64) (web.Todo, error) {
	t := web.Todo{ID: id, ByDay: map[string]bool{}}
	var rule string
	err := m.DB.QueryRowContext(ctx, "SELECT column_id, title, notes, coalesce(due_date, ''), completed_at IS NOT NULL, coalesce(repeat_rule, ''), repeat_from_completion FROM todos WHERE id = ?", id).
		Scan(&t.ColumnID, &t.Title, &t.Notes, &t.Due, &t.Done, &rule, &t.FromCompletion)
	if errors.Is(err, sql.ErrNoRows) {
		return t, errTodoGone
	} else if err != nil {
		return t, err
	}
	if p := decodeRepeat(rule); p.Freq != "" {
		t.Repeat, t.Interval = p.Freq, strconv.Itoa(p.Interval)
		for _, d := range p.ByDay {
			t.ByDay[d] = true
		}
	}
	return t, nil
}

// appendPosition is the place after the last to-do in column ?.
const appendPosition = "(SELECT coalesce(max(position) + 1, 0) FROM todos WHERE column_id = ?)"

// encodeDue is p's rule for a copy due on due (a date, or "").
func encodeDue(p todoRepeat, due string) any {
	d, _ := time.Parse(dateLayout, due)
	return nullable(p.Encode(d))
}

// Create adds t last in its column, repeating as p.
func (m todos) Create(ctx context.Context, t web.Todo, p todoRepeat) (id int64, err error) {
	err = m.inTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "INSERT INTO todos (column_id, title, notes, due_date, position, created_at, repeat_rule, repeat_from_completion) SELECT id, ?, ?, ?, "+appendPosition+", ?, ?, ? FROM todo_columns WHERE id = ? RETURNING id",
			t.Title, t.Notes, nullable(t.Due), t.ColumnID, m.Now().Unix(), encodeDue(p, t.Due), t.FromCompletion, t.ColumnID).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return errNoColumn
		}
		return err
	}, web.KindTodo)
	return id, err
}

// Edit saves t, repeating as p, over open to-do id; one moved to another
// column goes last there. Completed copies are never touched (spec §8).
func (m todos) Edit(ctx context.Context, id int64, t web.Todo, p todoRepeat) error {
	return m.inTx(ctx, func(tx *sql.Tx) error {
		if err := columnExists(ctx, tx, t.ColumnID); err != nil {
			return err
		}
		// The form has no anchor: an edit keeping the due date and repeat keeps the stored one.
		var rule, due string
		if err := tx.QueryRowContext(ctx, "SELECT coalesce(repeat_rule, ''), coalesce(due_date, '') FROM todos WHERE id = ?", id).Scan(&rule, &due); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if old := decodeRepeat(rule); due == t.Due && old.Freq == p.Freq && old.Interval == p.Interval && slices.Equal(old.ByDay, p.ByDay) {
			p.anchor = old.anchor
		}
		res, err := tx.ExecContext(ctx, `UPDATE todos SET title = ?, notes = ?, due_date = ?,
			position = CASE WHEN column_id = ? THEN position ELSE `+appendPosition+` END,
			column_id = ?, repeat_rule = ?, repeat_from_completion = ? WHERE id = ? AND completed_at IS NULL`,
			t.Title, t.Notes, nullable(t.Due), t.ColumnID, t.ColumnID, t.ColumnID, encodeDue(p, t.Due), t.FromCompletion, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return whyUnchanged(ctx, tx, id, errTodoCompleted)
		}
		return nil
	}, web.KindTodo)
}

// columnExists is errNoColumn unless column col exists.
func columnExists(ctx context.Context, tx *sql.Tx, col int64) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM todo_columns WHERE id = ?)", col).Scan(&exists); err != nil {
		return err
	} else if !exists {
		return errNoColumn
	}
	return nil
}

// whyUnchanged is why a guarded write to to-do id changed nothing: errTodoGone
// if it doesn't exist, else guard.
func whyUnchanged(ctx context.Context, tx *sql.Tx, id int64, guard error) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM todos WHERE id = ?)", id).Scan(&exists); err != nil {
		return err
	} else if !exists {
		return errTodoGone
	}
	return guard
}

// Complete completes open to-do id; a Recurring to-do's completion also
// creates its next copy (returned), cloned from this one. A completed to-do
// is refused, so the next copy is created exactly once.
func (m todos) Complete(ctx context.Context, id int64) (next int64, err error) {
	now := m.Now()
	err = m.inTx(ctx, func(tx *sql.Tx) error {
		var rule sql.NullString
		var fromCompletion bool
		var due string
		err := tx.QueryRowContext(ctx, "UPDATE todos SET completed_at = ? WHERE id = ? AND completed_at IS NULL RETURNING repeat_rule, repeat_from_completion, coalesce(due_date, '')", now.Unix(), id).
			Scan(&rule, &fromCompletion, &due)
		if errors.Is(err, sql.ErrNoRows) {
			return whyUnchanged(ctx, tx, id, errTodoCompleted)
		} else if err != nil || !rule.Valid {
			return err
		}
		d, err := time.Parse(dateLayout, due)
		if err != nil && !fromCompletion {
			return err
		}
		nextDue, nextRule, err := decodeRepeat(rule.String).Next(d, now.In(m.location(ctx)), fromCompletion)
		if err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `INSERT INTO todos (column_id, title, notes, due_date, position, created_at, repeat_rule, repeat_from_completion)
			SELECT column_id, title, notes, ?, position, ?, ?, repeat_from_completion FROM todos WHERE id = ? RETURNING id`, nextDue, now.Unix(), nextRule, id).Scan(&next); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE todos SET next_id = ? WHERE id = ?", next, id)
		return err
	}, web.KindTodo)
	if err != nil {
		return 0, err
	}
	return next, nil
}

// Reopen un-completes to-do id. If its completion created a next copy that
// is still open, that copy is deleted (and returned); a completed next copy
// must be re-opened first.
func (m todos) Reopen(ctx context.Context, id int64) (removed int64, err error) {
	err = m.inTx(ctx, func(tx *sql.Tx) error {
		var next sql.NullInt64
		err := tx.QueryRowContext(ctx, "SELECT next_id FROM todos WHERE id = ?", id).Scan(&next)
		if errors.Is(err, sql.ErrNoRows) {
			return errTodoGone
		} else if err != nil {
			return err
		}
		if next.Valid {
			res, err := tx.ExecContext(ctx, "DELETE FROM todos WHERE id = ? AND completed_at IS NULL", next.Int64)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return errNextCompleted
			}
			removed = next.Int64
		}
		_, err = tx.ExecContext(ctx, "UPDATE todos SET completed_at = NULL, next_id = NULL WHERE id = ?", id)
		return err
	}, web.KindTodo)
	if err != nil {
		return 0, err
	}
	return removed, nil
}

// Delete deletes to-do id, refusing a completed copy whose completion made
// the next copy: removing it would cut the chain, so re-opening its
// predecessor could leave two open copies.
func (m todos) Delete(ctx context.Context, id int64) error {
	return m.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM todos WHERE id = ? AND next_id IS NULL", id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return whyUnchanged(ctx, tx, id, errTodoChained)
		}
		return nil
	}, web.KindTodo)
}

// Reorder sets column col's order to ids, moving in any to-do dragged from
// another column. To-dos left out (completed ones) keep their old positions;
// ties fall back to id.
func (m todos) Reorder(ctx context.Context, col int64, ids []int64) error {
	return m.inTx(ctx, func(tx *sql.Tx) error {
		if err := columnExists(ctx, tx, col); err != nil {
			return err
		}
		for i, id := range ids {
			res, err := tx.ExecContext(ctx, "UPDATE todos SET column_id = ?, position = ? WHERE id = ?", col, i, id)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return errTodoGone
			}
		}
		return nil
	}, web.KindTodo)
}

// Columns is the board's columns in order, without their to-dos.
func (m todos) Columns(ctx context.Context) ([]web.TodoColumn, error) {
	rows, err := m.DB.QueryContext(ctx, "SELECT id, name FROM todo_columns ORDER BY position, id")
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

// AddColumn adds a column named name, last on the board.
func (m todos) AddColumn(ctx context.Context, name string) (id int64, err error) {
	err = m.inTx(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, "INSERT INTO todo_columns (name, position) VALUES (?, (SELECT coalesce(max(position) + 1, 0) FROM todo_columns)) RETURNING id", name).Scan(&id)
	}, web.KindTodo)
	return id, err
}

// RenameColumn renames column id, or returns errNoColumn.
func (m todos) RenameColumn(ctx context.Context, id int64, name string) error {
	return columnChanged(m.exec(ctx, web.KindTodo, "UPDATE todo_columns SET name = ? WHERE id = ?", name, id))
}

// DeleteColumn deletes column id with its to-dos, or returns errNoColumn.
func (m todos) DeleteColumn(ctx context.Context, id int64) error {
	return columnChanged(m.exec(ctx, web.KindTodo, "DELETE FROM todo_columns WHERE id = ?", id))
}

func columnChanged(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNoColumn
	}
	return nil
}

// todoRepeat is a Recurring to-do's repeat preset, stored as an RRULE.
type todoRepeat struct {
	Freq     string   // daily, weekly, monthly or yearly; "" when it doesn't repeat
	Interval int      // at least 1
	ByDay    []string // weekly only: MO, TU, ...
	anchor   string   // the stored rule's month-end anchor (see anchorRule), kept along the chain
}

// decodeRepeat reads a stored preset rule (FREQ, INTERVAL, BYDAY, then any anchor).
func decodeRepeat(rule string) todoRepeat {
	p := todoRepeat{Interval: 1}
	if i := strings.Index(rule, ";BYMONTH"); i >= 0 {
		rule, p.anchor = rule[:i], rule[i:]
	}
	for _, part := range strings.Split(rule, ";") {
		k, v, _ := strings.Cut(part, "=")
		switch k {
		case "FREQ":
			p.Freq = strings.ToLower(v)
		case "INTERVAL":
			if n, err := strconv.Atoi(v); err == nil {
				p.Interval = n
			}
		case "BYDAY":
			p.ByDay = strings.Split(v, ",")
		}
	}
	return p
}

// Encode is the rule a copy due on due stores: a monthly or yearly repeat
// keeps to the month's last day when due falls on its end, unless the repeat
// already carries an anchor from earlier in the chain. "" when it doesn't repeat.
func (p todoRepeat) Encode(due time.Time) string {
	if p.Freq == "" {
		return ""
	}
	rule := "FREQ=" + strings.ToUpper(p.Freq) + ";INTERVAL=" + strconv.Itoa(p.Interval)
	if len(p.ByDay) > 0 {
		rule += ";BYDAY=" + strings.Join(p.ByDay, ",")
	}
	if p.anchor != "" {
		return rule + p.anchor
	}
	return anchorRule(rule, due)
}

// Next is the due date and rule of the copy after one due on due and
// completed on completedOn. Repeat from schedule takes the first rule date
// after both, so missed dates are skipped and early completion doesn't use up
// the next one; Repeat from completion counts one interval from the
// completion day, and re-anchors there. A chain made before the anchor
// existed gains it here.
func (p todoRepeat) Next(due, completedOn time.Time, fromCompletion bool) (nextDue, nextRule string, err error) {
	day := time.Date(completedOn.Year(), completedOn.Month(), completedOn.Day(), 0, 0, 0, 0, time.UTC)
	if fromCompletion {
		p.anchor = ""
		n := p.Interval
		var next time.Time
		switch p.Freq {
		case "yearly":
			next = addMonthsClamped(day, 12*n)
		case "monthly":
			next = addMonthsClamped(day, n)
		case "weekly":
			next = day.AddDate(0, 0, 7*n)
		default:
			next = day.AddDate(0, 0, n)
		}
		return next.Format(dateLayout), p.Encode(day), nil
	}
	nextRule = p.Encode(due)
	opt, err := rrule.StrToROption(nextRule)
	if err != nil {
		return "", "", err
	}
	if day.Before(due) {
		day = due
	}
	nextDue, err = ruleDate(opt, due, day, false)
	return nextDue, nextRule, err
}
