// Package apptest builds the real app for tests: a temp SQLite file, a fake
// clock and fake upstream URLs, served over httptest.
package apptest

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/app"
	"github.com/DeRusto/goCasa/internal/config"
)

// Clock is a fake clock; tests Set or Advance it.
type Clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *Clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *Clock) Set(t time.Time)         { c.mu.Lock(); defer c.mu.Unlock(); c.t = t }
func (c *Clock) Advance(d time.Duration) { c.mu.Lock(); defer c.mu.Unlock(); c.t = c.t.Add(d) }

type Harness struct {
	App    *app.App
	Server *httptest.Server
	Clock  *Clock
	Client *http.Client // one browser: its own cookies, follows redirects
}

// Option adjusts the app's options before it is built, e.g. to point an
// upstream at an httptest fake.
type Option func(*app.Options)

func WithUpstreams(u app.Upstreams) Option { return func(o *app.Options) { o.Upstreams = u } }

// Start is the clock's starting time: a fixed instant so tests are repeatable.
var Start = time.Date(2026, 3, 15, 9, 0, 0, 0, time.UTC)

// New builds the app on a fresh temp DB. Upstreams default to an unroutable
// address so a test that forgets a fake fails instead of calling the internet.
func New(t testing.TB, opts ...Option) *Harness {
	t.Helper()
	clock := &Clock{t: Start}
	nowhere := "http://127.0.0.1:1"
	o := app.Options{
		Config: config.Config{
			Addr:          ":0",
			BaseURL:       &url.URL{Scheme: "http", Host: "gocasa.test"},
			DataDir:       t.TempDir(),
			Location:      time.UTC,
			EncryptionKey: make([]byte, 32),
			SessionSecret: []byte(strings.Repeat("s", 32)),
		},
		Now:       clock.Now,
		Upstreams: app.Upstreams{GoogleAPI: nowhere, GoogleOAuth: nowhere, OpenMeteo: nowhere},
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, opt := range opts {
		opt(&o)
	}
	a, err := app.New(context.Background(), o)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	srv := httptest.NewServer(a.Handler())
	t.Cleanup(func() { srv.Close(); a.Close() })
	h := &Harness{App: a, Server: srv, Clock: clock}
	return h.Device()
}

// Device returns the same app seen from another browser, with an empty cookie jar.
func (h *Harness) Device() *Harness {
	jar, _ := cookiejar.New(nil)
	d := *h
	d.Client = &http.Client{Jar: jar}
	return &d
}

// RunDue runs the scheduler's due work now, at the fake clock's time.
func (h *Harness) RunDue() { h.App.Scheduler.RunDue(context.Background()) }

// Get fetches path and returns the status and body.
func (h *Harness) Get(t testing.TB, path string) (int, string) {
	t.Helper()
	res, err := h.Client.Get(h.Server.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	return read(t, res)
}

// Post submits form to path as a same-origin browser would.
func (h *Harness) Post(t testing.TB, path string, form url.Values) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.Server.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	res, err := h.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return read(t, res)
}

func read(t testing.TB, res *http.Response) (int, string) {
	t.Helper()
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(b)
}
