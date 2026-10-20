package app_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/DeRusto/goCasa/internal/app"
	"github.com/DeRusto/goCasa/internal/apptest"
)

// fakeMeteo is Open-Meteo's forecast endpoint; it records each query, and
// answers 503 while down.
type fakeMeteo struct {
	mu      sync.Mutex
	queries []url.Values
	down    bool
	*httptest.Server
}

const meteoBody = `{"current":{"time":"2026-03-15T09:00","temperature_2m":12.4,"weather_code":3},
"daily":{"time":["2026-03-14","2026-03-15","2026-03-16"],"temperature_2m_max":[9.0,15.6,17.1],"temperature_2m_min":[1.0,7.5,8.2],"weather_code":[61,3,0]}}`

func newMeteo(t *testing.T) *fakeMeteo {
	m := &fakeMeteo{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.queries = append(m.queries, r.URL.Query())
		down := m.down
		m.mu.Unlock()
		if down {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path != "/v1/forecast" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(meteoBody))
	}))
	t.Cleanup(m.Close)
	return m
}

func (m *fakeMeteo) calls() []url.Values {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]url.Values(nil), m.queries...)
}

func weatherHousehold(t *testing.T) (*apptest.Harness, *fakeMeteo) {
	t.Helper()
	m := newMeteo(t)
	h := apptest.New(t, apptest.WithUpstreams(app.Upstreams{GoogleAuth: "http://127.0.0.1:1", GoogleAPI: "http://127.0.0.1:1", GoogleOAuth: "http://127.0.0.1:1", OpenMeteo: m.URL}))
	setup(t, h)
	return h, m
}

func TestWeatherPillRendersFromCacheEvenOffline(t *testing.T) {
	h, m := weatherHousehold(t)
	if code, page := h.Post(t, "/settings/weather", url.Values{"lat": {"51.5"}, "lon": {"-0.12"}, "units": {"metric"}}); code != http.StatusOK || !strings.Contains(page, `value="51.5"`) {
		t.Fatalf("save weather = %d %q", code, page)
	}
	if _, page := h.Get(t, "/calendar"); strings.Contains(page, `aria-label="Weather"`) {
		t.Fatal("pill before anything was fetched")
	}
	h.RunDue()
	q := m.calls()
	if len(q) != 1 || q[0].Get("latitude") != "51.5" || q[0].Get("longitude") != "-0.12" || q[0].Get("temperature_unit") != "" || q[0].Get("timezone") != "UTC" {
		t.Fatalf("queries = %v", q)
	}
	m.mu.Lock()
	m.down = true // the internet goes down; the fake still counts attempts
	m.mu.Unlock()
	for _, path := range []string{"/calendar", "/settings", "/"} {
		_, page := h.Get(t, path)
		if !strings.Contains(page, "12°C") || !strings.Contains(page, "H 16° L 8°") {
			t.Errorf("%s: no pill in %q", path, page)
		}
	}
	if len(m.calls()) != 1 {
		t.Fatal("a page fetched the weather")
	}
}

func TestWeatherImperialAsksForFahrenheit(t *testing.T) {
	h, m := weatherHousehold(t)
	h.Post(t, "/settings/weather", url.Values{"lat": {"40.7"}, "lon": {"-74"}, "units": {"imperial"}})
	h.RunDue()
	if q := m.calls(); len(q) != 1 || q[0].Get("temperature_unit") != "fahrenheit" {
		t.Fatalf("queries = %v", q)
	}
	if _, page := h.Get(t, "/calendar"); !strings.Contains(page, "12°F") {
		t.Fatalf("no °F pill in %q", page)
	}
	// Switching units hides the old units' cache until the next fetch.
	h.Post(t, "/settings/weather", url.Values{"lat": {"40.7"}, "lon": {"-74"}, "units": {"metric"}})
	if _, page := h.Get(t, "/calendar"); strings.Contains(page, `aria-label="Weather"`) {
		t.Fatal("pill in the old units")
	}
	h.RunDue()
	if _, page := h.Get(t, "/calendar"); !strings.Contains(page, "12°C") {
		t.Fatalf("no °C pill after refetch in %q", page)
	}
}

func TestWeatherNotFetchedWithoutALocation(t *testing.T) {
	h, m := weatherHousehold(t)
	h.RunDue()
	if len(m.calls()) != 0 {
		t.Fatal("fetched with no location")
	}
}

func TestWeatherSettingsValidated(t *testing.T) {
	h, _ := weatherHousehold(t)
	for _, f := range []url.Values{
		{"lat": {"91"}, "lon": {"0"}, "units": {"metric"}},
		{"lat": {"0"}, "lon": {"181"}, "units": {"metric"}},
		{"lat": {"x"}, "lon": {"0"}, "units": {"metric"}},
		{"lat": {"0"}, "lon": {"0"}, "units": {"kelvin"}},
		{"lat": {"0"}, "lon": {""}, "units": {"metric"}},
	} {
		if code, _ := h.Post(t, "/settings/weather", f); code != http.StatusBadRequest {
			t.Errorf("%v = %d", f, code)
		}
	}
	locked := h.Device()
	if code, page := locked.Post(t, "/settings/weather", url.Values{"lat": {"1"}, "lon": {"1"}, "units": {"metric"}}); code != http.StatusOK || !strings.Contains(page, lockPage) {
		t.Fatalf("locked save = %d", code)
	}
}

func TestWeatherPlaceSearchPicksALocation(t *testing.T) {
	geo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/search" || r.URL.Query().Get("name") != "London" {
			w.Write([]byte(`{}`)) // Open-Meteo leaves out results when nothing matches
			return
		}
		w.Write([]byte(`{"results":[{"name":"London","latitude":51.50853,"longitude":-0.12574,"admin1":"England","country":"United Kingdom"},
{"name":"London","latitude":42.98339,"longitude":-81.23304,"admin1":"Ontario","country":"Canada"}]}`))
	}))
	t.Cleanup(geo.Close)
	nowhere := "http://127.0.0.1:1"
	h := apptest.New(t, apptest.WithUpstreams(app.Upstreams{GoogleAuth: nowhere, GoogleAPI: nowhere, GoogleOAuth: nowhere, OpenMeteo: nowhere, Geocoding: geo.URL}))
	setup(t, h)

	_, page := h.Get(t, "/settings/weather/places?q=London")
	for _, want := range []string{"London, England, United Kingdom", `value="51.50853"`, "London, Ontario, Canada"} {
		if !strings.Contains(page, want) {
			t.Fatalf("results lack %q: %s", want, page)
		}
	}
	if _, page := h.Get(t, "/settings/weather/places?q=Nowhereville"); !strings.Contains(page, "No places match") {
		t.Fatalf("no-match page = %s", page)
	}
	h.Post(t, "/settings/weather", url.Values{"lat": {"51.50853"}, "lon": {"-0.12574"}, "place": {"London, England, United Kingdom"}, "units": {"metric"}})
	if _, page := h.Get(t, "/settings"); !strings.Contains(page, "<strong>London, England, United Kingdom</strong>") {
		t.Fatal("settings don't name the picked place")
	}
	h.Post(t, "/settings/weather", url.Values{"units": {"metric"}})
	if _, page := h.Get(t, "/settings"); !strings.Contains(page, "weather is off") {
		t.Fatal("turning weather off kept the place")
	}
	if code, page := h.Device().Get(t, "/settings/weather/places?q=London"); code != http.StatusOK || !strings.Contains(page, lockPage) {
		t.Fatalf("locked search = %d", code)
	}
}
