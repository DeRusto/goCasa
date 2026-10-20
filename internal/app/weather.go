package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
	"github.com/a-h/templ"
)

// openMeteo fetches the current conditions and daily forecast for the
// household's location into weather_cache (spec §10). Pages read only the cache.
type openMeteo struct{}

const openMeteoType = "open_meteo"

var meteoClient = feedClient

func (openMeteo) Sync(ctx context.Context, a *App, _ integrationRow) error {
	hh, err := a.household(ctx)
	if err != nil {
		return err
	}
	lat, lon, units := hh.WeatherLat, hh.WeatherLon, hh.Units
	if !lat.Valid || !lon.Valid {
		return nil
	}
	// Daily dates in the household timezone, so they match the pages' today.
	tz := hh.Zone.String()
	if tz == "Local" { // an unnamed zone: the location's own
		tz = "auto"
	}
	q := url.Values{
		"latitude":      {strconv.FormatFloat(lat.Float64, 'f', -1, 64)},
		"longitude":     {strconv.FormatFloat(lon.Float64, 'f', -1, 64)},
		"current":       {"temperature_2m,weather_code"},
		"daily":         {"temperature_2m_max,temperature_2m_min,weather_code"},
		"timezone":      {tz},
		"forecast_days": {"6"}, // today and 5 days ahead, for Today
	}
	if units == "imperial" {
		q.Set("temperature_unit", "fahrenheit")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Upstreams.OpenMeteo+"/v1/forecast?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "GoCasa")
	res, err := meteoClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("Open-Meteo answered %s", res.Status)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, new(forecast)); err != nil {
		return fmt.Errorf("Open-Meteo's answer isn't a forecast: %w", err)
	}
	_, err = a.exec(ctx, web.KindIntegration, `INSERT INTO weather_cache (id, lat, lon, units, fetched_at, body) VALUES (1, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET lat = excluded.lat, lon = excluded.lon, units = excluded.units, fetched_at = excluded.fetched_at, body = excluded.body`,
		lat.Float64, lon.Float64, units, a.Now().Unix(), string(body))
	return err
}

// forecast is the part of Open-Meteo's answer the pages use.
type forecast struct {
	Current struct {
		Temperature float64 `json:"temperature_2m"`
		Code        int     `json:"weather_code"`
	} `json:"current"`
	Daily struct {
		Time []string  `json:"time"` // YYYY-MM-DD in the location's timezone
		Max  []float64 `json:"temperature_2m_max"`
		Min  []float64 `json:"temperature_2m_min"`
		Code []int     `json:"weather_code"`
	} `json:"daily"`
}

// weatherPill is the cached weather as the slim pill shows it; nil when there
// is none for the current location and units.
func (a *App) weatherPill(ctx context.Context) (*web.WeatherPill, error) {
	h, err := a.weatherHeader(ctx)
	if h == nil {
		return nil, err
	}
	return &h.WeatherPill, nil
}

// weatherHeader is the cached weather as Today's header shows it: today and up
// to 5 days ahead. nil when there is none for the current location and units.
func (a *App) weatherHeader(ctx context.Context) (*web.WeatherHeader, error) {
	hh, err := a.household(ctx)
	if err != nil {
		return nil, err
	}
	units, body := hh.Units, ""
	err = a.DB.QueryRowContext(ctx, "SELECT body FROM weather_cache WHERE lat = ? AND lon = ? AND units = ?", hh.WeatherLat, hh.WeatherLon, units).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f forecast
	if err := json.Unmarshal([]byte(body), &f); err != nil {
		return nil, err
	}
	unit := "C"
	if units == "imperial" {
		unit = "F"
	}
	h := &web.WeatherHeader{WeatherPill: web.WeatherPill{Current: int(math.Round(f.Current.Temperature)), Unit: unit}, Conditions: conditions(f.Current.Code)}
	today := hh.Today(a.Now()).Format(dateLayout)
	for i, d := range f.Daily.Time {
		if d < today || i >= len(f.Daily.Max) || i >= len(f.Daily.Min) {
			continue
		}
		hi, lo := int(math.Round(f.Daily.Max[i])), int(math.Round(f.Daily.Min[i]))
		day, err := time.Parse(dateLayout, d)
		switch {
		case d == today:
			h.HasToday, h.High, h.Low = true, hi, lo
		case err == nil && len(h.Ahead) < 5:
			wd := web.WeatherDay{Day: day.Format("Mon"), High: hi, Low: lo}
			if i < len(f.Daily.Code) {
				wd.Conditions = conditions(f.Daily.Code[i])
			}
			h.Ahead = append(h.Ahead, wd)
		}
	}
	return h, nil
}

// conditions names a WMO weather code as Open-Meteo reports it.
func conditions(code int) string {
	switch {
	case code == 0:
		return "Clear"
	case code <= 2:
		return "Partly cloudy"
	case code == 3:
		return "Overcast"
	case code <= 48:
		return "Fog"
	case code <= 57:
		return "Drizzle"
	case code <= 67, code >= 80 && code <= 82:
		return "Rain"
	case code <= 77, code == 85, code == 86:
		return "Snow"
	case code >= 95:
		return "Thunderstorm"
	}
	return ""
}

func (openMeteo) Section(ctx context.Context, a *App, st map[int64]integrationHealth, _ []web.Member) (templ.Component, error) {
	w, err := a.weatherSettings(ctx, st)
	return web.WeatherSection(w), err
}

// weatherSettings is the Weather section of Settings.
func (a *App) weatherSettings(ctx context.Context, st map[int64]integrationHealth) (web.Weather, error) {
	hh, err := a.household(ctx)
	if err != nil {
		return web.Weather{}, err
	}
	w := web.Weather{Units: hh.Units, Place: hh.WeatherPlace}
	lat, lon := hh.WeatherLat, hh.WeatherLon
	var id int64
	err = a.DB.QueryRowContext(ctx, "SELECT id FROM integrations WHERE type = ?", openMeteoType).Scan(&id)
	if lat.Valid && lon.Valid {
		w.Lat, w.Lon = strconv.FormatFloat(lat.Float64, 'f', -1, 64), strconv.FormatFloat(lon.Float64, 'f', -1, 64)
	}
	x, ok := st[id]
	if !ok {
		x.LastSuccess = "never"
	}
	w.Failures, w.LastError, w.LastSuccess = x.Failures, x.LastError, x.LastSuccess
	return w, err
}

// saveWeather sets the weather location and units; blank lat and lon turn weather off.
func (a *App) saveWeather(w http.ResponseWriter, r *http.Request) {
	units := r.PostFormValue("units")
	rawLat, rawLon := strings.TrimSpace(r.PostFormValue("lat")), strings.TrimSpace(r.PostFormValue("lon"))
	place := strings.TrimSpace(r.PostFormValue("place"))
	var lat, lon sql.NullFloat64
	if rawLat != "" || rawLon != "" {
		var err1, err2 error
		lat.Float64, err1 = strconv.ParseFloat(rawLat, 64)
		lon.Float64, err2 = strconv.ParseFloat(rawLon, 64)
		if err1 != nil || err2 != nil || math.Abs(lat.Float64) > 90 || math.Abs(lon.Float64) > 180 {
			a.settingsPage(w, r, http.StatusBadRequest, "Latitude is -90 to 90 and longitude -180 to 180, or leave both blank for no weather.")
			return
		}
		lat.Valid, lon.Valid = true, true
	} else {
		place = ""
	}
	if len(place) > 200 {
		a.settingsPage(w, r, http.StatusBadRequest, "That place name is too long.")
		return
	}
	if units != "metric" && units != "imperial" {
		a.settingsPage(w, r, http.StatusBadRequest, "Pick metric or imperial units.")
		return
	}
	err := a.inTx(r.Context(), func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(), "UPDATE household_settings SET weather_lat = ?, weather_lon = ?, weather_place = ?, units = ?", lat, lon, place, units); err != nil {
			return err
		}
		// Fetch on the next scheduler tick, not now: pages never wait on Open-Meteo.
		_, err := tx.ExecContext(r.Context(), "UPDATE integrations SET next_sync_at = ? WHERE type = ?", a.Now().Unix(), openMeteoType)
		return err
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}

// searchPlaces lists the places Open-Meteo's geocoding finds for q, for the
// Weather section to pick from. Failures render as a message, not an error
// status, so htmx still swaps them in.
func (a *App) searchPlaces(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.FormValue("q"))
	hh, err := a.household(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	res := web.PlaceResults{Units: hh.Units}
	if q != "" {
		res.Places, err = a.geocode(r.Context(), q)
		switch {
		case err != nil:
			a.Log.Error("searching places", "err", err)
			res.Message = "Couldn't reach Open-Meteo's place search. Try again in a moment."
		case len(res.Places) == 0:
			res.Message = "No places match “" + q + "”. Try just the town or city name."
		}
	}
	a.render(w, r, http.StatusOK, web.PlaceList(res))
}

// geocode is up to 8 places matching name, best first.
func (a *App) geocode(ctx context.Context, name string) ([]web.Place, error) {
	q := url.Values{"name": {name}, "count": {"8"}, "language": {"en"}, "format": {"json"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.Upstreams.Geocoding+"/v1/search?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "GoCasa")
	res, err := meteoClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Open-Meteo geocoding answered %s", res.Status)
	}
	var body struct {
		Results []struct {
			Name                string
			Latitude, Longitude float64
			Admin1, Country     string
		}
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("Open-Meteo geocoding's answer isn't a place list: %w", err)
	}
	places := make([]web.Place, 0, len(body.Results))
	for _, p := range body.Results {
		label := p.Name
		for _, part := range []string{p.Admin1, p.Country} {
			if part != "" && part != p.Name {
				label += ", " + part
			}
		}
		places = append(places, web.Place{
			Name: label,
			Lat:  strconv.FormatFloat(p.Latitude, 'f', -1, 64),
			Lon:  strconv.FormatFloat(p.Longitude, 'f', -1, 64),
		})
	}
	return places, nil
}
