package app

import (
	"context"
	"database/sql"
	"errors"
	"strconv"

	"github.com/DeRusto/goCasa/internal/web"
)

// memberTodoCap is how many to-dos a Member page shows; the rest wait on the To-dos page.
const memberTodoCap = 5

var errNotPickable = errors.New("not a Member a column can belong to")

// columnMemberField reads the column form's Member: keep is true when the
// form has no such field, member is 0 for no one.
func columnMemberField(form map[string][]string) (member int64, keep bool, ok bool) {
	v, has := form["member_id"]
	if !has {
		return 0, true, true
	}
	if len(v) == 0 || v[0] == "" {
		return 0, false, true
	}
	member, err := strconv.ParseInt(v[0], 10, 64)
	return member, false, err == nil && member > 0
}

// SetColumnMember gives column id to Member member, or to no one when member
// is 0. Only a Member with a Member page (active, not a pet) can be picked,
// though a column keeps an Archived Member it already belongs to.
func (m todos) SetColumnMember(ctx context.Context, id, member int64) error {
	return m.inTx(ctx, func(tx *sql.Tx) error {
		if member != 0 {
			var pickable bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM members WHERE id = ? AND NOT pet
				AND (NOT archived OR id = (SELECT member_id FROM todo_columns WHERE id = ?)))`, member, id).Scan(&pickable); err != nil {
				return err
			} else if !pickable {
				return errNotPickable
			}
		}
		res, err := tx.ExecContext(ctx, "UPDATE todo_columns SET member_id = ? WHERE id = ?", sql.NullInt64{Int64: member, Valid: member != 0}, id)
		return columnChanged(res, err)
	}, web.KindTodo)
}

// ForMember is the open to-dos of Member member's columns that are overdue,
// due today or undated, in that order, capped to memberTodoCap; more is how
// many the cap leaves out.
func (m todos) ForMember(ctx context.Context, member int64, today string) (ts []web.Todo, more int, err error) {
	rows, err := m.DB.QueryContext(ctx, `SELECT t.id, t.column_id, t.title, coalesce(t.due_date, '') FROM todos t JOIN todo_columns c ON c.id = t.column_id
		WHERE c.member_id = ? AND t.completed_at IS NULL AND (t.due_date IS NULL OR t.due_date <= ?)
		ORDER BY t.due_date IS NULL, t.due_date, c.position, c.id, t.position, t.id`, member, today)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var t web.Todo
		if err := rows.Scan(&t.ID, &t.ColumnID, &t.Title, &t.Due); err != nil {
			return nil, 0, err
		}
		t.Overdue = t.Due != "" && t.Due < today
		ts = append(ts, t)
	}
	if len(ts) > memberTodoCap {
		ts, more = ts[:memberTodoCap], len(ts)-memberTodoCap
	}
	return ts, more, rows.Err()
}
