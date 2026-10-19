// Package app wires GoCasa into one http.Handler. main and the tests build it
// the same way, so tests drive the real app over HTTP.
package app

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/DeRusto/goCasa/internal/config"
	"github.com/DeRusto/goCasa/internal/db"
)

// Upstreams are the fixed third-party base URLs; tests point them at fakes.
// Per-integration URLs (Immich, Mealie, iCal feeds) live on the integration row.
type Upstreams struct {
	GoogleAuth  string // the consent page the browser is sent to
	GoogleAPI   string
	GoogleOAuth string
	OpenMeteo   string
}

func DefaultUpstreams() Upstreams {
	return Upstreams{
		GoogleAuth:  "https://accounts.google.com",
		GoogleAPI:   "https://www.googleapis.com",
		GoogleOAuth: "https://oauth2.googleapis.com",
		OpenMeteo:   "https://api.open-meteo.com",
	}
}

type Options struct {
	Config    config.Config
	Now       func() time.Time // nil = time.Now
	Upstreams Upstreams
	Logger    *slog.Logger // nil = slog.Default()
}

type App struct {
	Config    config.Config
	DB        *sql.DB
	Now       func() time.Time
	Upstreams Upstreams
	Log       *slog.Logger
	Scheduler *Scheduler
	Bus       *Bus
	handler   http.Handler
	syncLocks sync.Map // integration id → *sync.Mutex; see syncIntegration
}

// New opens DATA_DIR/app.db, runs migrations and builds the handler.
func New(ctx context.Context, o Options) (*App, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if err := os.MkdirAll(o.Config.DataDir, 0o750); err != nil {
		return nil, err
	}
	d, err := db.Open(filepath.Join(o.Config.DataDir, "app.db"))
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// Named by schema version so a same-day second upgrade keeps the first
	// rollback point; never pruned (see dailyBackups).
	preMigration := func(from int) error {
		now := o.Now()
		return backup(ctx, d, backupDir(o.Config.DataDir), backupDay(now, o.Config.Location)+fmt.Sprintf("-pre-v%d.db", from), now)
	}
	if err := db.Migrate(ctx, d, db.Migrations, preMigration); err != nil {
		d.Close()
		return nil, err
	}
	a := &App{
		Config:    o.Config,
		DB:        d,
		Now:       o.Now,
		Upstreams: o.Upstreams,
		Log:       o.Logger,
		Bus:       newBus(),
	}
	a.Scheduler = &Scheduler{now: o.Now, log: o.Logger}
	if err := a.startBackups(ctx); err != nil {
		d.Close()
		return nil, fmt.Errorf("startup backup: %w", err)
	}
	a.Scheduler.Add("integrations", func(time.Time) bool { return true }, a.syncDue)
	a.handler = a.routes()
	return a, nil
}

func (a *App) Handler() http.Handler { return a.handler }

func (a *App) Close() error { return a.DB.Close() }

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.healthz)
	mux.HandleFunc("GET /events/stream", a.eventStream)
	mux.HandleFunc("GET /{$}", a.getRoot)
	mux.HandleFunc("GET /today", a.getToday)
	mux.HandleFunc("POST /device/default-view", a.setDefaultView)
	mux.HandleFunc("POST /device/theme", a.setTheme)
	mux.HandleFunc("POST /settings/household", a.requireUnlock(a.saveHousehold))
	mux.HandleFunc("GET /settings", a.getSettings)
	mux.HandleFunc("POST /setup", a.postSetup)
	mux.HandleFunc("GET /lock", a.getLock)
	mux.HandleFunc("POST /lock", a.postLock)
	mux.HandleFunc("POST /lock/now", a.postLockNow)
	mux.HandleFunc("POST /settings/members", a.requireUnlock(a.createMember))
	mux.HandleFunc("POST /settings/members/{id}", a.requireUnlock(a.updateMember))
	mux.HandleFunc("POST /settings/members/{id}/delete", a.requireUnlock(a.deleteMember))
	mux.HandleFunc("POST /settings/members/{id}/avatar", a.requireUnlock(a.uploadAvatar))
	mux.HandleFunc("POST /settings/members/{id}/avatar/delete", a.requireUnlock(a.deleteAvatar))
	mux.HandleFunc("GET /members/{id}/avatar", a.getAvatar)
	mux.HandleFunc("POST /settings/weather", a.requireUnlock(a.saveWeather))
	mux.HandleFunc("POST /settings/feeds", a.requireUnlock(a.addFeed))
	mux.HandleFunc("POST /settings/feeds/{id}/delete", a.requireUnlock(a.removeFeed))
	mux.HandleFunc("POST /integrations/google/connect", a.requireUnlock(a.connectGoogle))
	mux.HandleFunc("GET /auth/google/callback", a.requireUnlock(a.googleCallback))
	mux.HandleFunc("POST /settings/google/{id}/delete", a.requireUnlock(a.disconnectGoogle))
	mux.HandleFunc("POST /settings/google-calendars/{id}", a.requireUnlock(a.updateGoogleCalendar))
	mux.HandleFunc("POST /settings/mealie", a.requireUnlock(a.connectMealie))
	mux.HandleFunc("POST /settings/mealie/delete", a.requireUnlock(a.disconnectMealie))
	mux.HandleFunc("POST /settings/mealie-lists/{id}", a.requireUnlock(a.hideMealieList))
	mux.HandleFunc("POST /settings/members/{id}/archive", a.requireUnlock(a.setArchived(true)))
	mux.HandleFunc("POST /settings/members/{id}/restore", a.requireUnlock(a.setArchived(false)))
	mux.HandleFunc("POST /settings/locks", a.requireUnlock(a.saveLocks))
	mux.HandleFunc("GET /calendar", a.getCalendar)
	mux.HandleFunc("GET /calendar.ics", a.getICS)
	mux.HandleFunc("POST /calendar/calendars", a.requireLock("lock_calendar", a.createCalendar))
	mux.HandleFunc("GET /calendar/event/new", a.newEvent)
	mux.HandleFunc("POST /calendar/event", a.requireLock("lock_calendar", a.saveEvent))
	mux.HandleFunc("GET /calendar/event/{id}", a.getEvent)
	mux.HandleFunc("POST /calendar/event/{id}", a.requireLock("lock_calendar", a.saveEvent))
	mux.HandleFunc("POST /calendar/event/{id}/delete", a.requireLock("lock_calendar", a.deleteEvent))
	mux.HandleFunc("GET /calendar/synced/{id}", a.getSynced)
	mux.HandleFunc("POST /calendar/synced/{id}", a.requireLock("lock_calendar", a.saveSynced))
	mux.HandleFunc("POST /calendar/synced/{id}/edit", a.requireLock("lock_calendar", a.saveGoogle))
	mux.HandleFunc("POST /calendar/synced/{id}/delete", a.requireLock("lock_calendar", a.deleteGoogle))
	mux.HandleFunc("POST /calendar/sync", a.syncNow)
	mux.HandleFunc("GET /lists", a.getLists)
	mux.HandleFunc("POST /lists", a.createList)
	mux.HandleFunc("POST /lists/{id}/items", a.addItem)
	mux.HandleFunc("POST /lists/{list}/items/{id}", a.checkItem)
	mux.HandleFunc("POST /lists/{list}/items/{id}/move", a.moveItem)
	mux.HandleFunc("POST /lists/{list}/items/{id}/delete", a.deleteItem)
	mux.HandleFunc("POST /lists/{id}/clear", a.clearChecked)
	mux.HandleFunc("GET /todos", a.getTodos)
	mux.HandleFunc("POST /todos", a.createTodo)
	mux.HandleFunc("POST /todos/reorder", a.reorderTodos)
	mux.HandleFunc("GET /todos/{id}", a.getTodo)
	mux.HandleFunc("POST /todos/{id}", a.updateTodo)
	mux.HandleFunc("POST /todos/{id}/complete", a.requireLock("lock_todos", a.completeTodo))
	mux.HandleFunc("POST /todos/{id}/reopen", a.requireLock("lock_todos", a.reopenTodo))
	mux.HandleFunc("POST /todos/{id}/delete", a.deleteTodo)
	mux.HandleFunc("GET /chores", a.getChores)
	mux.HandleFunc("POST /chores", a.requireUnlock(a.saveChore))
	mux.HandleFunc("GET /chores/{id}", a.requireUnlock(a.getChore))
	mux.HandleFunc("POST /chores/{id}", a.requireUnlock(a.saveChore))
	mux.HandleFunc("POST /chores/{id}/archive", a.requireUnlock(a.setChoreArchived(true)))
	mux.HandleFunc("POST /chores/{id}/restore", a.requireUnlock(a.setChoreArchived(false)))
	mux.HandleFunc("POST /chores/{id}/complete", a.requireLock("lock_chores", a.completeChore))
	mux.HandleFunc("POST /chores/completions/{id}/delete", a.requireUnlock(a.undoCompletion))
	mux.HandleFunc("POST /rewards", a.requireUnlock(a.createReward))
	mux.HandleFunc("POST /rewards/{id}/archive", a.requireUnlock(a.setRewardArchived(true)))
	mux.HandleFunc("POST /rewards/{id}/restore", a.requireUnlock(a.setRewardArchived(false)))
	mux.HandleFunc("POST /rewards/{id}/redeem", a.requireLock("lock_chores", a.redeem))
	mux.HandleFunc("POST /redemptions/{id}/approve", a.requireUnlock(a.decide("approved")))
	mux.HandleFunc("POST /redemptions/{id}/decline", a.requireUnlock(a.decide("declined")))
	mux.HandleFunc("POST /adjustments", a.requireUnlock(a.adjust))
	mux.HandleFunc("POST /todo-columns", a.createTodoColumn)
	mux.HandleFunc("POST /todo-columns/{id}", a.renameTodoColumn)
	mux.HandleFunc("POST /todo-columns/{id}/delete", a.deleteTodoColumn)

	// CSRF: refuse cross-site unsafe requests by Sec-Fetch-Site/Origin (no tokens to thread through forms or htmx).
	csrf := http.NewCrossOriginProtection()
	if err := csrf.AddTrustedOrigin(a.Config.BaseURL.Scheme + "://" + a.Config.BaseURL.Host); err != nil {
		panic(err) // BASE_URL is validated by config.Load
	}
	return csrf.Handler(a.withUnlock(mux))
}

func (a *App) healthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := a.DB.QueryRowContext(ctx, "SELECT 1").Scan(new(int)); err != nil {
		a.Log.Error("healthz: db ping failed", "err", err)
		http.Error(w, "db unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok\n"))
}
