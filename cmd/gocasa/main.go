// Command gocasa serves the GoCasa family dashboard.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	_ "time/tzdata" // DST rules never depend on the host image

	"github.com/DeRusto/goCasa/internal/app"
	"github.com/DeRusto/goCasa/internal/config"
	"github.com/DeRusto/goCasa/internal/db"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if len(os.Args) > 1 && os.Args[1] == "reset-pin" {
		os.Exit(resetPIN(log, os.Args[2:]))
	}
	if err := run(log); err != nil {
		log.Error("gocasa stopped", "err", err)
		os.Exit(1)
	}
}

// healthcheck probes the local /healthz for Docker; the scratch image has no curl.
func healthcheck() int {
	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = config.DefaultAddr
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return 1
	}
	if host == "" {
		host = "127.0.0.1"
	}
	c := http.Client{Timeout: 3 * time.Second}
	res, err := c.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		return 1
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// resetPIN is `gocasa reset-pin <member>`: run inside the container when the
// last Admin forgets their PIN. It prints a new PIN to change in Settings.
func resetPIN(log *slog.Logger, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: gocasa reset-pin <Admin name or id>")
		return 2
	}
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		log.Error("reset-pin", "err", err)
		return 1
	}
	// Just the database: no migrations or backups under the running server.
	d, err := db.Open(filepath.Join(cfg.DataDir, "app.db"))
	if err != nil {
		log.Error("reset-pin", "err", err)
		return 1
	}
	defer d.Close()
	pin, err := (&app.App{DB: d}).ResetPIN(context.Background(), args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "reset-pin:", err)
		return 1
	}
	fmt.Printf("New PIN for %s: %s\nUnlock Settings with it, then choose your own.\n", args[0], pin)
	return 0
}

func run(log *slog.Logger) error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	a, err := app.New(ctx, app.Options{Config: cfg, Upstreams: app.DefaultUpstreams(), Logger: log})
	if err != nil {
		return err
	}
	defer a.Close()
	schedDone := make(chan struct{})
	go func() { a.Scheduler.Run(ctx, 30*time.Second); close(schedDone) }()
	defer func() { stop(); <-schedDone }() // runs before a.Close: no job sees a closed DB

	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: a.Handler(), ReadHeaderTimeout: 10 * time.Second}
	srv.RegisterOnShutdown(a.Bus.Close) // SSE streams never finish on their own
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Info("gocasa listening", "addr", cfg.Addr, "base_url", cfg.BaseURL.String())

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
