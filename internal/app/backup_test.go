package app_test

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"github.com/DeRusto/goCasa/internal/app"
	"github.com/DeRusto/goCasa/internal/apptest"
	"github.com/DeRusto/goCasa/internal/db"
)

func backups(t *testing.T, a *app.App) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Join(a.Config.DataDir, "backups"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func TestNightlyBackupAt0300KeepsLast14(t *testing.T) {
	h := apptest.New(t) // starts 2026-03-15 09:00 UTC
	h.Clock.Set(time.Date(2026, 3, 16, 2, 59, 0, 0, time.UTC))
	h.RunDue()
	if got := backups(t, h.App); slices.Contains(got, "app-20260316.db") {
		t.Fatalf("backup before 03:00: %v", got)
	}
	h.Clock.Set(time.Date(2026, 3, 16, 3, 0, 0, 0, time.UTC))
	h.RunDue()
	if got := backups(t, h.App); !slices.Contains(got, "app-20260316.db") {
		t.Fatalf("no backup at 03:00: %v", got)
	}

	for range 20 {
		h.Clock.Advance(24 * time.Hour)
		h.RunDue()
	}
	got := backups(t, h.App)
	if len(got) != 14 || got[0] != "app-20260323.db" || got[13] != "app-20260405.db" {
		t.Fatalf("backups = %v, want the 14 newest (0323..0405)", got)
	}
}

func TestStartupBackupOnlyWhenNewestOver24h(t *testing.T) {
	h := apptest.New(t) // fresh install: no backup yet, so one is taken at Start
	if got := backups(t, h.App); !slices.Equal(got, []string{"app-20260315.db"}) {
		t.Fatalf("backups after first start = %v", got)
	}
	h.App.Close()

	restart := func(at time.Time) {
		t.Helper()
		a, err := app.New(context.Background(), app.Options{Config: h.App.Config, Logger: h.App.Log,
			Now: func() time.Time { return at }})
		if err != nil {
			t.Fatal(err)
		}
		a.Close()
	}
	restart(apptest.Start.Add(23 * time.Hour))
	if got := backups(t, h.App); len(got) != 1 {
		t.Fatalf("backup taken under 24h: %v", got)
	}
	restart(apptest.Start.Add(25 * time.Hour))
	if got := backups(t, h.App); !slices.Equal(got, []string{"app-20260315.db", "app-20260316.db"}) {
		t.Fatalf("no backup over 24h: %v", got)
	}
}

func TestPreMigrationBackupIsKeptOutOfPruning(t *testing.T) {
	h := apptest.New(t)
	h.App.Close()
	orig := db.Migrations
	t.Cleanup(func() { db.Migrations = orig })
	// The real migrations, plus one more after them.
	files, err := fs.Glob(orig, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	mfs := fstest.MapFS{}
	for _, f := range files {
		b, err := fs.ReadFile(orig, f)
		if err != nil {
			t.Fatal(err)
		}
		mfs[f] = &fstest.MapFile{Data: b}
	}
	mfs[fmt.Sprintf("%04d_test.sql", len(files)+1)] = &fstest.MapFile{Data: []byte("CREATE TABLE upgraded (x INTEGER);")}
	db.Migrations = mfs
	pre := fmt.Sprintf("app-20260315-pre-v%d.db", len(files))
	a, err := app.New(context.Background(), app.Options{Config: h.App.Config, Logger: h.App.Log, Now: h.Clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if got := backups(t, a); !slices.Contains(got, pre) {
		t.Fatalf("no pre-migration backup: %v", got)
	}

	h.App.Scheduler = a.Scheduler
	for range 20 {
		h.Clock.Advance(24 * time.Hour)
		h.RunDue()
	}
	if got := backups(t, a); len(got) != 15 || !slices.Contains(got, pre) {
		t.Fatalf("backups = %v, want 14 dailies plus the pre-migration one", got)
	}
}
