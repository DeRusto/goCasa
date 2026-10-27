package app_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/app"
	"github.com/DeRusto/goCasa/internal/apptest"
)

func TestUnknownPathIs404(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	if code, _ := h.Get(t, "/nope"); code != http.StatusNotFound {
		t.Fatalf("GET /nope = %d, want 404", code)
	}
}

func TestHealthzPingsDB(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	if code, body := h.Get(t, "/healthz"); code != http.StatusOK || body != "ok\n" {
		t.Fatalf("healthz = %d %q", code, body)
	}
	h.App.DB.Close()
	if code, _ := h.Get(t, "/healthz"); code != http.StatusServiceUnavailable {
		t.Fatalf("healthz with closed DB = %d, want 503", code)
	}
}

func TestRefusesDatabaseNewerThanBinary(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	if _, err := h.App.DB.Exec("PRAGMA user_version = 9999"); err != nil {
		t.Fatal(err)
	}
	h.App.Close()

	_, err := app.New(context.Background(), app.Options{Config: h.App.Config, Logger: h.App.Log})
	if err == nil || !strings.Contains(err.Error(), "newer than this binary") {
		t.Fatalf("app.New err = %v, want refusal", err)
	}
}

func TestRunDueRunsOnlyDueJobsAtTheFakeClock(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	at := apptest.Start.Add(time.Hour)
	var ran []time.Time
	h.App.Scheduler.Add("hourly", func(now time.Time) bool { return !now.Before(at) },
		func(_ context.Context, now time.Time) error { ran = append(ran, now); return nil })

	h.RunDue()
	if len(ran) != 0 {
		t.Fatal("job ran before it was due")
	}
	h.Clock.Advance(time.Hour)
	h.RunDue()
	if len(ran) != 1 || !ran[0].Equal(at) {
		t.Fatalf("ran = %v, want once at %v", ran, at)
	}
}

func TestFailingJobDoesNotStopOthers(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	always := func(time.Time) bool { return true }
	h.App.Scheduler.Add("panics", always, func(context.Context, time.Time) error { panic("boom") })
	h.App.Scheduler.Add("errors", always, func(context.Context, time.Time) error { return errors.New("nope") })
	ok := false
	h.App.Scheduler.Add("ok", always, func(context.Context, time.Time) error { ok = true; return nil })
	h.RunDue()
	if !ok {
		t.Fatal("later job did not run")
	}
}
