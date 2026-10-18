package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func openTemp(t *testing.T) *sql.DB {
	t.Helper()
	d, err := Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func userVersion(t *testing.T, d *sql.DB) int {
	t.Helper()
	var v int
	if err := d.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func files(m map[string]string) fstest.MapFS {
	fs := fstest.MapFS{}
	for k, v := range m {
		fs[k] = &fstest.MapFile{Data: []byte(v)}
	}
	return fs
}

func TestOpenUsesWAL(t *testing.T) {
	d := openTemp(t)
	var mode string
	if err := d.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

func TestMigrateAppliesInNumericOrder(t *testing.T) {
	d := openTemp(t)
	// 0003 needs 0002's table, so out-of-order application fails.
	m := files(map[string]string{
		"0001_a.sql": "CREATE TABLE a (x INTEGER);",
		"0002_b.sql": "CREATE TABLE b (x INTEGER);",
		"0003_c.sql": "INSERT INTO b VALUES (3);",
	})
	if err := Migrate(context.Background(), d, m, nil); err != nil {
		t.Fatal(err)
	}
	if v := userVersion(t, d); v != 3 {
		t.Fatalf("user_version = %d, want 3", v)
	}
	// Re-running applies nothing new (0003 would insert a second row).
	if err := Migrate(context.Background(), d, m, nil); err != nil {
		t.Fatal(err)
	}
	var n int
	d.QueryRow("SELECT count(*) FROM b").Scan(&n)
	if n != 1 {
		t.Fatalf("rows in b = %d, want 1", n)
	}
}

func TestMigrateAppliesOnlyNewMigrations(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	if err := Migrate(ctx, d, files(map[string]string{"0001_a.sql": "CREATE TABLE a (x INTEGER);"}), nil); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, d, files(map[string]string{
		"0001_a.sql": "CREATE TABLE a (x INTEGER);",
		"0002_b.sql": "CREATE TABLE b (x INTEGER);",
	}), nil); err != nil {
		t.Fatal(err)
	}
	if v := userVersion(t, d); v != 2 {
		t.Fatalf("user_version = %d, want 2", v)
	}
}

func TestFailedMigrationRollsBackItsOwnTransactionOnly(t *testing.T) {
	d := openTemp(t)
	err := Migrate(context.Background(), d, files(map[string]string{
		"0001_a.sql": "CREATE TABLE a (x INTEGER);",
		"0002_b.sql": "CREATE TABLE b (x INTEGER); INSERT INTO nope VALUES (1);",
	}), nil)
	if err == nil || !strings.Contains(err.Error(), "0002_b.sql") {
		t.Fatalf("err = %v, want failure naming 0002_b.sql", err)
	}
	if v := userVersion(t, d); v != 1 {
		t.Fatalf("user_version = %d, want 1 (0001 committed, 0002 rolled back)", v)
	}
	var n int
	d.QueryRow("SELECT count(*) FROM sqlite_master WHERE name = 'b'").Scan(&n)
	if n != 0 {
		t.Fatal("table b exists: 0002 was not rolled back")
	}
}

func TestMigrateRefusesNewerDatabase(t *testing.T) {
	d := openTemp(t)
	if _, err := d.Exec("PRAGMA user_version = 3"); err != nil {
		t.Fatal(err)
	}
	err := Migrate(context.Background(), d, files(map[string]string{
		"0001_a.sql": "CREATE TABLE a (x INTEGER);",
		"0002_b.sql": "CREATE TABLE b (x INTEGER);",
	}), nil)
	if err == nil || !strings.Contains(err.Error(), "newer than this binary") {
		t.Fatalf("err = %v, want refusal", err)
	}
	if v := userVersion(t, d); v != 3 {
		t.Fatalf("user_version changed to %d", v)
	}
}

func TestMigrateAcceptsDatabaseAtExactlyLatest(t *testing.T) {
	d := openTemp(t)
	d.Exec("PRAGMA user_version = 2")
	if err := Migrate(context.Background(), d, files(map[string]string{
		"0001_a.sql": "CREATE TABLE a (x INTEGER);",
		"0002_b.sql": "CREATE TABLE b (x INTEGER);",
	}), nil); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateRejectsGapsAndDuplicates(t *testing.T) {
	for name, m := range map[string]map[string]string{
		"gap":        {"0001_a.sql": "SELECT 1;", "0003_c.sql": "SELECT 1;"},
		"duplicate":  {"0001_a.sql": "SELECT 1;", "0001_b.sql": "SELECT 1;"},
		"unnumbered": {"init.sql": "SELECT 1;"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := Migrate(context.Background(), openTemp(t), files(m), nil); err == nil {
				t.Fatal("want error")
			}
		})
	}
}

func TestEmbeddedMigrationsApply(t *testing.T) {
	d := openTemp(t)
	if err := Migrate(context.Background(), d, Migrations, nil); err != nil {
		t.Fatal(err)
	}
	if userVersion(t, d) < 1 {
		t.Fatal("no embedded migrations applied")
	}
}

func TestBeforeHookRunsOnlyWhenAnExistingDatabaseHasPendingMigrations(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	calls := 0
	before := func(from int) error {
		calls++
		if from != 1 {
			t.Errorf("before got from=%d, want 1", from)
		}
		return nil
	}
	one := files(map[string]string{"0001_a.sql": "CREATE TABLE a (x INTEGER);"})
	two := files(map[string]string{"0001_a.sql": "CREATE TABLE a (x INTEGER);", "0002_b.sql": "CREATE TABLE b (x INTEGER);"})

	if err := Migrate(ctx, d, one, before); err != nil || calls != 0 {
		t.Fatalf("fresh database: err %v, hook calls %d, want 0 (nothing to protect)", err, calls)
	}
	if err := Migrate(ctx, d, two, before); err != nil || calls != 1 {
		t.Fatalf("pending migration: err %v, hook calls %d, want 1", err, calls)
	}
	if err := Migrate(ctx, d, two, before); err != nil || calls != 1 {
		t.Fatalf("nothing pending: err %v, hook calls %d, want still 1", err, calls)
	}
	if v := userVersion(t, d); v != 2 {
		t.Fatalf("user_version = %d", v)
	}
}

func TestFailingBeforeHookStopsMigrations(t *testing.T) {
	d := openTemp(t)
	ctx := context.Background()
	if err := Migrate(ctx, d, files(map[string]string{"0001_a.sql": "SELECT 1;"}), nil); err != nil {
		t.Fatal(err)
	}
	err := Migrate(ctx, d, files(map[string]string{"0001_a.sql": "SELECT 1;", "0002_b.sql": "SELECT 1;"}),
		func(int) error { return errors.New("disk full") })
	if err == nil || userVersion(t, d) != 1 {
		t.Fatalf("err %v, user_version %d: migration ran without its backup", err, userVersion(t, d))
	}
}
