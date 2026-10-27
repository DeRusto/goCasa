package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
)

// An event's Members come from three sources, each beating the next:
//   - a pick on the event in GoCasa (events.local_override);
//   - a pick for its Google Series from an Occurrence on (google_series_members);
//   - its Google calendar's Default Members (source_calendar_members).
//
// A pick, on the event or its Series, keeps Archived Members: the form lists
// only active ones, and a Series pick puts back the list chosen at the time.
// Default Members never add an Archived Member, nor remove one from an event
// that has them; their default stays stored for when they're restored.
//
// This file is the only code that reads or writes those three.

// defaultMembersJSON is source calendar c's Default Members as a JSON array, for a query on source_calendars c.
const defaultMembersJSON = "(SELECT json_group_array(member_id) FROM source_calendar_members WHERE calendar_id = c.id)"

// pickMembers gives rows Members members, picked in GoCasa: archived Members
// already on them stay, and later syncs keep the pick.
func pickMembers(ctx context.Context, tx *sql.Tx, rows, members []int64) error {
	ids, _ := json.Marshal(rows)
	if _, err := tx.ExecContext(ctx, "UPDATE events SET local_override = 1 WHERE id IN (SELECT value FROM json_each(?))", string(ids)); err != nil {
		return err
	}
	list, _ := json.Marshal(members)
	return replaceActive(ctx, tx, "SELECT value AS id FROM json_each(?1)", "SELECT value FROM json_each(?2)", string(ids), string(list))
}

// replaceActive replaces the active Members of the events evs selects by the
// active ones of the Members picked selects, both bound to args. Archived
// Members are neither added nor removed.
func replaceActive(ctx context.Context, tx *sql.Tx, evs, picked string, args ...any) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM event_members WHERE event_id IN ("+evs+") AND member_id IN (SELECT id FROM members WHERE NOT archived)", args...); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO event_members (event_id, member_id) SELECT e.id, m.id FROM (`+evs+`) e
		JOIN members m ON NOT m.archived AND m.id IN (`+picked+`)`, args...)
	return err
}

// pickSeriesMembers picks Members members for Google Series series on calendar
// cal from Occurrence row from on (0 for all of it): on its rows now, and for
// its Occurrences that sync later, even with none in the window yet.
func pickSeriesMembers(ctx context.Context, tx *sql.Tx, cal int64, series string, from int64, members []int64) error {
	rows, err := googleRows(ctx, tx, cal, series, from)
	if err != nil {
		return err
	}
	if err := pickMembers(ctx, tx, rows, members); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM google_series_members WHERE calendar_id = ?1 AND series_id = ?2
		AND (?3 = 0 OR from_key >= (SELECT `+startKey+` FROM events WHERE id = ?3))`, cal, series, from); err != nil {
		return err
	}
	list, _ := json.Marshal(members)
	_, err = tx.ExecContext(ctx, `INSERT INTO google_series_members (calendar_id, series_id, from_key, members)
		VALUES (?1, ?2, (SELECT `+startKey+` FROM events WHERE id = ?3), ?4)`, cal, series, from, string(list))
	return err
}

// googleRows is the rows of Google event or Series id on calendar cal, from row from's start on (0 for all).
func googleRows(ctx context.Context, tx *sql.Tx, cal int64, id string, from int64) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM events WHERE calendar_id = ?1 AND (external_id = ?2 OR external_series_id = ?2)
		AND (?3 = 0 OR `+startKey+` >= (SELECT `+startKey+` FROM events WHERE id = ?3))`, cal, id, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// forgetSeriesMembers drops the picks for Google Series series on calendar cal, once Google deletes it.
func forgetSeriesMembers(ctx context.Context, tx *sql.Tx, cal int64, series string) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM google_series_members WHERE calendar_id = ? AND series_id = ?", cal, series)
	return err
}

// setDefaultMembers sets calendar cal's Default Members to members, keeping
// archived ones stored for their return, and re-applies them.
func setDefaultMembers(ctx context.Context, tx *sql.Tx, cal int64, members []int64) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM source_calendar_members WHERE calendar_id = ? AND member_id IN (SELECT id FROM members WHERE NOT archived)", cal); err != nil {
		return err
	}
	list, _ := json.Marshal(members)
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO source_calendar_members SELECT ?, id FROM members
		WHERE NOT archived AND id IN (SELECT value FROM json_each(?))`, cal, string(list)); err != nil {
		return err
	}
	return applyDefaults(ctx, tx, cal, nil)
}

// resolveMembers gives calendar cal's rows, just synced, their Members: those
// picked for their Series from their start on, else the Default Members.
// Rows picked on the event keep theirs.
func resolveMembers(ctx context.Context, tx *sql.Tx, cal int64, rows []int64) error {
	list, _ := json.Marshal(rows)
	q, err := tx.QueryContext(ctx, `SELECT e.id, s.members FROM events e JOIN google_series_members s ON s.rowid = (
			SELECT rowid FROM google_series_members WHERE calendar_id = e.calendar_id AND series_id = e.external_series_id
				AND (from_key IS NULL OR from_key <= `+startKey+`) ORDER BY from_key DESC LIMIT 1)
		WHERE e.calendar_id = ? AND e.id IN (SELECT value FROM json_each(?)) AND NOT e.local_override`, cal, string(list))
	if err != nil {
		return err
	}
	picked := map[int64]string{}
	for q.Next() {
		var id int64
		var members string
		if err := q.Scan(&id, &members); err != nil {
			q.Close()
			return err
		}
		picked[id] = members
	}
	q.Close()
	if err := q.Err(); err != nil {
		return err
	}
	for id, ms := range picked {
		if _, err := tx.ExecContext(ctx, "UPDATE events SET local_override = 1 WHERE id = ?", id); err != nil {
			return err
		}
		if err := replaceActive(ctx, tx, "SELECT ?1 AS id", "SELECT value FROM json_each(?2)", id, ms); err != nil {
			return err
		}
		// Archived since the pick or not: a Member picked stays picked, as on the rows that had it then.
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO event_members (event_id, member_id) SELECT ?, id FROM members WHERE id IN (SELECT value FROM json_each(?))", id, ms); err != nil {
			return err
		}
	}
	return applyDefaults(ctx, tx, cal, rows)
}

// applyDefaults gives calendar cal's events with no pick their Default Members:
// just those in only, or with a nil only all of them.
func applyDefaults(ctx context.Context, tx *sql.Tx, cal int64, only []int64) error {
	var ids any
	if only != nil {
		list, _ := json.Marshal(only)
		ids = string(list)
	}
	return replaceActive(ctx, tx, "SELECT id FROM events WHERE calendar_id = ?1 AND NOT local_override AND (?2 IS NULL OR id IN (SELECT value FROM json_each(?2)))",
		"SELECT member_id FROM source_calendar_members WHERE calendar_id = ?1", cal, ids)
}

// membersChanged reports whether picked and was differ in their active Members:
// archived ones can't be picked, so they don't count.
func (a *App) membersChanged(ctx context.Context, picked, was []int64) (bool, error) {
	active, err := a.activeMemberIDs(ctx)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(active, func(m int64) bool { return slices.Contains(picked, m) != slices.Contains(was, m) }), nil
}

// activeMemberIDs is the ids of the Members who aren't archived.
func (a *App) activeMemberIDs(ctx context.Context) ([]int64, error) {
	rows, err := a.DB.QueryContext(ctx, "SELECT id FROM members WHERE NOT archived")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
