// Package db opens the SQLite file and runs the embedded migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var embedded embed.FS

// Migrations is the binary's embedded migration set.
var Migrations, _ = fs.Sub(embedded, "migrations")

// Open opens (creating if needed) the SQLite file at path in WAL mode.
func Open(file string) (*sql.DB, error) {
	dsn := "file:" + file + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)"
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := d.Ping(); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// Migrate applies every file in migrations named NNNN_*.sql whose number is
// above PRAGMA user_version, in order, one transaction each. It refuses a DB
// whose user_version is newer than the highest migration.
func Migrate(ctx context.Context, d *sql.DB, migrations fs.FS) error {
	files, err := fs.Glob(migrations, "*.sql")
	if err != nil {
		return err
	}
	type mig struct {
		n    int
		name string
	}
	var ms []mig
	for _, f := range files {
		n, err := strconv.Atoi(strings.SplitN(f, "_", 2)[0])
		if err != nil || n < 1 {
			return fmt.Errorf("migration %q: name must start with a positive number", f)
		}
		ms = append(ms, mig{n, f})
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].n < ms[j].n })
	for i, m := range ms {
		if m.n != i+1 {
			return fmt.Errorf("migration %q: expected number %d (no gaps or duplicates)", m.name, i+1)
		}
	}

	var current int
	if err := d.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return err
	}
	if current > len(ms) {
		return fmt.Errorf("database schema version %d is newer than this binary (%d); run a newer GoCasa or restore a backup", current, len(ms))
	}
	for _, m := range ms[current:] {
		body, err := fs.ReadFile(migrations, m.name)
		if err != nil {
			return err
		}
		if err := apply(ctx, d, m.n, string(body)); err != nil {
			return fmt.Errorf("migration %q: %w", m.name, err)
		}
	}
	return nil
}

func apply(ctx context.Context, d *sql.DB, n int, body string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return err
	}
	// user_version is transactional in SQLite, so it commits with the migration.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", n)); err != nil {
		return err
	}
	return tx.Commit()
}
