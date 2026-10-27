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
		"current":       {"temperature_2m,weather_code,apparent_temperature,relative_humidity_2m,wind_speed_10m,is_day"},
		"hourly":        {"temperature_2m,weather_code,precipitation_probability,is_day"},
		"daily":         {"temperature_2m_max,temperature_2m_min,weather_code,precipitation_probability_max,sunrise,sunset,uv_index_max"},
		"timezone":      {tz},
		"forecast_days": {"6"}, // today and 5 days ahead, for Today
	}
	if units == "imperial" {
		q.Set("temperature_unit", "fahrenheit")
		q.Set("wind_speed_unit", "mph")
	}
	var body json.RawMessage
	sc := syncClient{name: "Open-Meteo", client: meteoClient, limit: 1 << 20}
	if err := sc.json(ctx, http.MethodGet, a.Upstreams.OpenMeteo+"/v1/forecast?"+q.Encode(), nil, &body); err != nil {
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

// forecast is the part of Open-Meteo's answer the pages use. A cache fetched
// before the Weather page existed lacks its fields; they read as zero.
type forecast struct {
	Current struct {
		Time        string  `json:"time"` // YYYY-MM-DDTHH:MM in the location's timezone
		Temperature float64 `json:"temperature_2m"`
		Code        int     `json:"weather_code"`
		Feels       float64 `json:"apparent_temperature"`
		Humidity    float64 `json:"relative_humidity_2m"`
		Wind        float64 `json:"wind_speed_10m"`
		IsDay       int     `json:"is_day"`
	} `json:"current"`
	Hourly struct {
		Time  []string  `json:"time"`
		Temp  []float64 `json:"temperature_2m"`
		Code  []int     `json:"weather_code"`
		Rain  []float64 `json:"precipitation_probability"`
		IsDay []int     `json:"is_day"`
	} `json:"hourly"`
	Daily struct {
		Time    []string  `json:"time"` // YYYY-MM-DD in the location's timezone
		Max     []float64 `json:"temperature_2m_max"`
		Min     []float64 `json:"temperature_2m_min"`
		Code    []int     `json:"weather_code"`
		Rain    []float64 `json:"precipitation_probability_max"`
		Sunrise []string  `json:"sunrise"`
		Sunset  []string  `json:"sunset"`
		UV      []float64 `json:"uv_index_max"`
	} `json:"daily"`
}

// cachedForecast is the cached forecast for the household's location and
// units; nil when there is none.
func (a *App) cachedForecast(ctx context.Context) (Household, *forecast, error) {
	hh, err := a.household(ctx)
	if err != nil {
		return hh, nil, err
	}
	var body string
	err = a.DB.QueryRowContext(ctx, "SELECT body FROM weather_cache WHERE lat = ? AND lon = ? AND units = ?", hh.WeatherLat, hh.WeatherLon, hh.Units).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return hh, nil, nil
	}
	if err != nil {
		return hh, nil, err
	}
	f := new(forecast)
	return hh, f, json.Unmarshal([]byte(body), f)
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
	hh, f, err := a.cachedForecast(ctx)
	if f == nil || err != nil {
		return nil, err
	}
	unit := "C"
	if hh.Units == "imperial" {
		unit = "F"
	}
	h := &web.WeatherHeader{WeatherPill: web.WeatherPill{Current: int(math.Round(f.Current.Temperature)), Unit: unit,
		Conditions: conditions(f.Current.Code), Day: f.Current.IsDay == 1 || f.Current.Time == ""}}
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
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second) // someone is waiting, unlike the background fetch
	defer cancel()
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

// getWeather is the full-screen Weather page, from the cache like the pill.
func (a *App) getWeather(w http.ResponseWriter, r *http.Request) {
	hh, f, err := a.cachedForecast(r.Context())
	if err != nil {
		a.Log.Error("reading cached weather", "err", err) // the page still renders, without weather
		f = nil
	}
	p := web.WeatherPage{Place: hh.WeatherPlace, Off: !hh.WeatherLat.Valid}
	if f != nil {
		p.Now = weatherNow(hh, f, hh.Today(a.Now()).Format(dateLayout))
	}
	a.render(w, r, http.StatusOK, web.WeatherFull(p))
}

// weatherNow is f as the Weather page shows it: now, the next 24 hours, and
// today onwards.
func weatherNow(hh Household, f *forecast, today string) *web.WeatherNow {
	c := f.Current
	n := &web.WeatherNow{
		Temp: round(c.Temperature), Unit: "C", Conditions: conditions(c.Code), Day: c.IsDay == 1,
		Feels: round(c.Feels), Humidity: round(c.Humidity), Wind: round(c.Wind), WindUnit: "km/h",
	}
	if hh.Units == "imperial" {
		n.Unit, n.WindUnit = "F", "mph"
	}
	clock, hour := hh.Clock(), "15:04"
	if !hh.Clock24 {
		hour = "3 PM"
	}
	// Open-Meteo's times are local wall-clock times with no zone; parse them as UTC and only reformat.
	at := func(layout, s string) string {
		t, err := time.Parse("2006-01-02T15:04", s)
		if err != nil {
			return ""
		}
		return t.Format(layout)
	}
	from := c.Time
	if len(from) >= 13 {
		from = from[:13] + ":00" // the current hour
	}
	hr := f.Hourly
	for i, t := range hr.Time {
		if t < from || i >= len(hr.Temp) || len(n.Hours) == 24 {
			continue
		}
		h := web.WeatherHour{Time: at(hour, t), Temp: round(hr.Temp[i]), Day: true}
		if i < len(hr.Code) {
			h.Conditions = conditions(hr.Code[i])
		}
		if i < len(hr.Rain) {
			h.Rain = round(hr.Rain[i])
		}
		if i < len(hr.IsDay) {
			h.Day = hr.IsDay[i] == 1
		}
		n.Hours = append(n.Hours, h)
	}
	d := f.Daily
	lo, hi, ranges := math.Inf(1), math.Inf(-1), [][2]float64(nil)
	for i, t := range d.Time {
		if t < today || i >= len(d.Max) || i >= len(d.Min) {
			continue
		}
		day := web.WeatherDayRange{High: round(d.Max[i]), Low: round(d.Min[i])}
		if p, err := time.Parse(dateLayout, t); err == nil {
			day.Day = p.Format("Mon")
		}
		if t == today {
			day.Day = "Today"
			n.HasToday, n.High, n.Low = true, day.High, day.Low
			if i < len(d.Sunrise) && i < len(d.Sunset) {
				n.Sunrise, n.Sunset = at(clock, d.Sunrise[i]), at(clock, d.Sunset[i])
			}
			if i < len(d.UV) {
				n.UV = round(d.UV[i])
			}
		}
		if i < len(d.Code) {
			day.Conditions = conditions(d.Code[i])
		}
		if i < len(d.Rain) {
			day.Rain = round(d.Rain[i])
		}
		if t == today {
			n.Rain = day.Rain
		}
		lo, hi = math.Min(lo, d.Min[i]), math.Max(hi, d.Max[i])
		ranges = append(ranges, [2]float64{d.Min[i], d.Max[i]})
		n.Days = append(n.Days, day)
	}
	// Each day's bar spans its low to high on a scale of the whole week's.
	for i, r := range ranges {
		if span := hi - lo; span > 0 {
			n.Days[i].Left, n.Days[i].Width = (r[0]-lo)/span*100, (r[1]-r[0])/span*100
		}
	}
	return n
}

func round(f float64) int { return int(math.Round(f)) }
