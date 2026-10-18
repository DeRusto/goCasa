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
	"time"

	"github.com/DeRusto/goCasa/internal/config"
	"github.com/DeRusto/goCasa/internal/db"
	"github.com/DeRusto/goCasa/internal/web"
)

// Upstreams are the fixed third-party base URLs; tests point them at fakes.
// Per-integration URLs (Immich, Mealie, iCal feeds) live on the integration row.
type Upstreams struct {
	GoogleAPI   string
	GoogleOAuth string
	OpenMeteo   string
}

func DefaultUpstreams() Upstreams {
	return Upstreams{
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
	handler   http.Handler
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
	if err := db.Migrate(ctx, d, db.Migrations); err != nil {
		d.Close()
		return nil, err
	}
	a := &App{
		Config:    o.Config,
		DB:        d,
		Now:       o.Now,
		Upstreams: o.Upstreams,
		Log:       o.Logger,
	}
	a.Scheduler = &Scheduler{now: o.Now, log: o.Logger}
	a.handler = a.routes()
	return a, nil
}

func (a *App) Handler() http.Handler { return a.handler }

func (a *App) Close() error { return a.DB.Close() }

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", a.healthz)
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		web.Home().Render(r.Context(), w)
	})
	return mux
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
