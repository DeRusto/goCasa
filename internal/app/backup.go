package app

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// keepBackups is how many backups survive pruning (spec §11, not configurable).
const keepBackups = 14

// backup writes VACUUM INTO dir/name, stamps it with now (so age checks follow
// the injected clock), then prunes the daily backups to the newest keepBackups.
func backup(ctx context.Context, d *sql.DB, dir, name string, now time.Time) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	// VACUUM INTO refuses an existing file; write aside, then rename over it.
	dst := filepath.Join(dir, name)
	tmp := dst + ".tmp"
	os.Remove(tmp)
	if _, err := d.ExecContext(ctx, "VACUUM INTO ?", tmp); err != nil {
		return fmt.Errorf("backup %s: %w", name, err)
	}
	if err := os.Chtimes(tmp, now, now); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	daily, err := dailyBackups(dir)
	if err != nil {
		return err
	}
	for _, f := range daily[:max(0, len(daily)-keepBackups)] {
		if err := os.Remove(f); err != nil {
			return err
		}
	}
	return nil
}

// dailyBackups lists dir's app-YYYYMMDD.db files, oldest first (Glob sorts by
// name, and the name is the date). Pre-migration backups don't match: they
// are rollback points and are never pruned.
func dailyBackups(dir string) ([]string, error) {
	return filepath.Glob(filepath.Join(dir, "app-[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9].db"))
}

func backupDir(dataDir string) string { return filepath.Join(dataDir, "backups") }

// backupDay is the "app-YYYYMMDD" stem for t's date in loc.
func backupDay(t time.Time, loc *time.Location) string { return "app-" + t.In(loc).Format("20060102") }

func (a *App) dailyBackup(ctx context.Context, now time.Time) error {
	return backup(ctx, a.DB, backupDir(a.Config.DataDir), backupDay(now, a.Config.Location)+".db", now)
}

// startBackups takes a backup now if the newest daily is over 24h old (or
// none exists), then schedules one at 03:00 local every day.
func (a *App) startBackups(ctx context.Context) error {
	now := a.Now()
	daily, err := dailyBackups(backupDir(a.Config.DataDir))
	if err != nil {
		return err
	}
	stale := len(daily) == 0
	if !stale {
		fi, err := os.Stat(daily[len(daily)-1])
		if err != nil {
			return err
		}
		stale = now.Sub(fi.ModTime()) > 24*time.Hour
	}
	if stale {
		if err := a.dailyBackup(ctx, now); err != nil {
			return err
		}
	}
	next := next0300(now, a.Config.Location)
	a.Scheduler.Add("nightly backup", func(now time.Time) bool { return !now.Before(next) },
		func(ctx context.Context, now time.Time) error {
			next = next0300(now, a.Config.Location)
			return a.dailyBackup(ctx, now)
		})
	return nil
}

// next0300 is the first 03:00 in loc strictly after t.
func next0300(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	n := time.Date(l.Year(), l.Month(), l.Day(), 3, 0, 0, 0, loc)
	if !n.After(t) {
		n = time.Date(l.Year(), l.Month(), l.Day()+1, 3, 0, 0, 0, loc)
	}
	return n
}
