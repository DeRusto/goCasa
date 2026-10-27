package app

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// addGoogleCalendar adds a Google account with one calendar to a and returns the calendar's id.
func addGoogleCalendar(t *testing.T, a *App) int64 {
	t.Helper()
	var cal int64
	err := a.DB.QueryRow(`INSERT INTO integrations (type, name) VALUES ('` + googleType + `', 'ada@example.com');
		INSERT INTO source_calendars (integration_id, name, color, external_id) VALUES (last_insert_rowid(), 'Ada', '#3b82f6', 'ada') RETURNING id`).Scan(&cal)
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

// inTestTx runs fn in a transaction on a, failing t if it errs.
func inTestTx(t *testing.T, a *App, fn func(tx *sql.Tx) error) {
	t.Helper()
	if err := a.inTx(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
}

// synced stores Google event id of series (none if "") on calendar cal, starting
// at start, as a sync would, and returns its row.
func synced(t *testing.T, a *App, cal int64, id, series string, start time.Time) int64 {
	t.Helper()
	var row int64
	inTestTx(t, a, func(tx *sql.Tx) error {
		err := tx.QueryRow(`INSERT INTO events (calendar_id, external_id, external_series_id, title, all_day, start_utc, end_utc, start_tzid, updated_at)
			VALUES (?, ?, nullif(?, ''), 'Swim', 0, ?, ?, 'UTC', 0)
			ON CONFLICT (calendar_id, external_id) WHERE external_id IS NOT NULL DO UPDATE SET title = excluded.title RETURNING id`,
			cal, id, series, start.Unix(), start.Add(time.Hour).Unix()).Scan(&row)
		if err != nil {
			return err
		}
		return resolveMembers(context.Background(), tx, cal, []int64{row})
	})
	return row
}

// membersOn is event row's Members, by id.
func membersOn(t *testing.T, a *App, row int64) string {
	t.Helper()
	var got sql.NullString
	if err := a.DB.QueryRow("SELECT group_concat(member_id, ',' ORDER BY member_id) FROM event_members WHERE event_id = ?", row).Scan(&got); err != nil {
		t.Fatal(err)
	}
	return got.String
}

func archive(t *testing.T, a *App, member int64, archived bool) {
	t.Helper()
	if _, err := a.DB.Exec("UPDATE members SET archived = ? WHERE id = ?", archived, member); err != nil {
		t.Fatal(err)
	}
}

func wantMembers(t *testing.T, a *App, want map[int64]string) {
	t.Helper()
	for row, w := range want {
		if got := membersOn(t, a, row); got != w {
			t.Errorf("row %d has Members %q, want %q", row, got, w)
		}
	}
}

// A pick on the event beats a pick for its Series from that Occurrence on,
// which beats the calendar's Default Members — through later syncs, a change
// of defaults, and an Occurrence Google sends back as a new row.
func TestEventMembersOrder(t *testing.T) {
	a := household(t)
	ctx := context.Background()
	cal := addGoogleCalendar(t, a)
	inTestTx(t, a, func(tx *sql.Tx) error { return setDefaultMembers(ctx, tx, cal, []int64{2}) })
	occ := make([]int64, 4)
	for i := range occ {
		occ[i] = synced(t, a, cal, fmt.Sprintf("swim_%d", i), "swim", at(16+i, 18))
	}
	wantMembers(t, a, map[int64]string{occ[0]: "2", occ[3]: "2"})

	inTestTx(t, a, func(tx *sql.Tx) error { return pickSeriesMembers(ctx, tx, cal, "swim", occ[1], []int64{3}) })
	inTestTx(t, a, func(tx *sql.Tx) error { return pickMembers(ctx, tx, []int64{occ[2]}, []int64{1}) })
	inTestTx(t, a, func(tx *sql.Tx) error { return setDefaultMembers(ctx, tx, cal, []int64{1, 2}) })
	for i := range occ { // a later sync
		synced(t, a, cal, fmt.Sprintf("swim_%d", i), "swim", at(16+i, 18))
	}
	later := synced(t, a, cal, "swim_4", "swim", at(20, 18))
	if _, err := a.DB.Exec("DELETE FROM events WHERE id = ?", occ[3]); err != nil {
		t.Fatal(err)
	}
	back := synced(t, a, cal, "swim_3", "swim", at(19, 18))
	wantMembers(t, a, map[int64]string{occ[0]: "1,2", occ[1]: "3", occ[2]: "1", later: "3", back: "3"})
}

// A pick, on the event or its Series, keeps Archived Members, also on
// Occurrences that sync later; Default Members never add an Archived Member,
// never take one off an event, and keep their default for their return.
func TestEventMembersArchived(t *testing.T) {
	a := household(t)
	ctx := context.Background()
	cal := addGoogleCalendar(t, a)
	inTestTx(t, a, func(tx *sql.Tx) error { return setDefaultMembers(ctx, tx, cal, []int64{2}) })
	older := synced(t, a, cal, "dentist", "", at(16, 9))
	picked := synced(t, a, cal, "vet", "", at(17, 9))
	inTestTx(t, a, func(tx *sql.Tx) error { return pickMembers(ctx, tx, []int64{picked}, []int64{2}) })
	synced(t, a, cal, "swim_0", "swim", at(16, 18))
	inTestTx(t, a, func(tx *sql.Tx) error { return pickSeriesMembers(ctx, tx, cal, "swim", 0, []int64{2}) })

	archive(t, a, 2, true)
	newer := synced(t, a, cal, "dentist_2", "", at(18, 9))
	laterSwim := synced(t, a, cal, "swim_1", "swim", at(17, 18))
	inTestTx(t, a, func(tx *sql.Tx) error { return pickMembers(ctx, tx, []int64{picked}, []int64{3}) })
	inTestTx(t, a, func(tx *sql.Tx) error { return setDefaultMembers(ctx, tx, cal, []int64{3}) })
	wantMembers(t, a, map[int64]string{older: "2,3", newer: "3", picked: "2,3", laterSwim: "2"})

	archive(t, a, 2, false)
	returned := synced(t, a, cal, "dentist_3", "", at(19, 9))
	wantMembers(t, a, map[int64]string{returned: "2,3"})
}

// Archived Members can't be picked: only active ones count as a change.
func TestMembersChangedComparesActiveMembers(t *testing.T) {
	a := household(t)
	archive(t, a, 2, true)
	for _, c := range []struct {
		picked, was []int64
		want        bool
	}{{[]int64{1}, []int64{1, 2}, false}, {[]int64{1, 3}, []int64{1, 2}, true}, {nil, []int64{1}, true}, {nil, nil, false}} {
		if got, err := a.membersChanged(context.Background(), c.picked, c.was); err != nil || got != c.want {
			t.Errorf("membersChanged(%v, %v) = %v, %v; want %v", c.picked, c.was, got, err, c.want)
		}
	}
}
