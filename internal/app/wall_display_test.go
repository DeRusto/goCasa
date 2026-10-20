package app_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/apptest"
)

const idleWatch = `data-idle-seconds="`

func TestAnAdminSwitchesThisBrowserIntoAWallDisplay(t *testing.T) {
	h := household(t)
	other := h.Device()
	if !asksForPIN(t, other, "/device/wall-display", url.Values{"wall_display": {"on"}}) {
		t.Fatal("a locked device became a Wall display")
	}
	if _, body := h.Get(t, "/today"); strings.Contains(body, idleWatch) {
		t.Fatal("a browser that isn't a Wall display watches for idle")
	}

	post(t, h, "/device/wall-display", url.Values{"wall_display": {"on"}})
	for _, page := range []string{"/today", "/calendar", "/todos", "/settings"} {
		if _, body := h.Get(t, page); !strings.Contains(body, idleWatch+`300"`) {
			t.Fatalf("%s on a Wall display doesn't go idle after 300 s: %q", page, body)
		}
	}
	if _, body := h.Get(t, "/settings"); !strings.Contains(body, `name="wall_display" checked`) || !strings.Contains(body, `id="wake-lock"`) {
		t.Fatalf("This device doesn't show the Wall display switch and wake lock status: %q", body)
	}
	if _, body := other.Get(t, "/today"); strings.Contains(body, idleWatch) {
		t.Fatal("another browser became a Wall display too")
	}

	post(t, h, "/device/wall-display", nil) // switch off
	if _, body := h.Get(t, "/today"); strings.Contains(body, idleWatch) {
		t.Fatal("switching off left the Wall display on")
	}
}

func TestTheHouseholdSetsTheScreensaverIdleTime(t *testing.T) {
	h := household(t)
	post(t, h, "/device/wall-display", url.Values{"wall_display": {"on"}})
	post(t, h, "/settings/household", url.Values{"default_view": {"today"}, "week_start": {"0"}, "clock_24h": {"on"}, "screensaver_idle_seconds": {"60"}})
	if _, body := h.Get(t, "/today"); !strings.Contains(body, idleWatch+`60"`) {
		t.Fatalf("idle time isn't 60 s: %q", body)
	}
	if _, body := h.Get(t, "/settings"); !strings.Contains(body, `name="screensaver_idle_seconds" value="60"`) {
		t.Fatalf("Settings doesn't show the idle time: %q", body)
	}
	// Browsers fire a setTimeout over about 24.8 days at once.
	for _, f := range []string{"screensaver_idle_seconds", "seconds_per_photo"} {
		if code, _ := h.Post(t, "/settings/household", url.Values{"default_view": {"today"}, "week_start": {"0"}, f: {"86401"}}); code != http.StatusBadRequest {
			t.Errorf("%s over a day = %d, want 400", f, code)
		}
	}
}

func TestTheScreensaverShowsClockAndTodaysAgendaAndEndsTheUnlock(t *testing.T) {
	h := household(t) // unlocked as Ada; now is Sunday 15 March 2026 09:00 UTC
	addEvent(t, h, timed("Dentist", "2026-03-15T15:00", "2026-03-15T16:00", "UTC"))
	addEvent(t, h, timed("Swim", "2026-03-16T10:00", "2026-03-16T11:00", "UTC"))

	_, body := h.Get(t, "/screensaver")
	for _, want := range []string{">09:00</time>", "Sunday 15 March", "Dentist"} {
		if !strings.Contains(body, want) {
			t.Fatalf("screensaver has no %q: %q", want, body)
		}
	}
	if !strings.Contains(body, `<a id="wake" href="/"`) {
		t.Fatalf("screensaver doesn't wake to the Default view: %q", body)
	}
	if strings.Contains(body, "Swim") {
		t.Fatal("screensaver shows tomorrow's agenda")
	}
	if !asksForPIN(t, h, "/settings/household", url.Values{"default_view": {"todos"}, "week_start": {"0"}}) {
		t.Fatal("the screensaver didn't end the Admin unlock")
	}
}

const holdsWakeLock = `navigator.wakeLock.request`

func nightMode(t *testing.T, h *apptest.Harness, mode string) {
	t.Helper()
	post(t, h, "/settings/household", url.Values{"default_view": {"today"}, "week_start": {"0"}, "clock_24h": {"on"},
		"night_mode": {mode}, "night_start": {"22"}, "night_end": {"6"}})
}

func TestNightModeIsOffByDefault(t *testing.T) {
	h := household(t)
	post(t, h, "/device/wall-display", url.Values{"wall_display": {"on"}})
	if _, body := h.Get(t, "/settings"); !strings.Contains(body, `<option value="off" selected`) {
		t.Fatalf("Night mode isn't Off by default: %q", body)
	}
	h.Clock.Set(time.Date(2026, 3, 15, 23, 0, 0, 0, time.UTC))
	addEvent(t, h, timed("Late call", "2026-03-15T23:30", "2026-03-15T23:45", "UTC"))
	if _, body := h.Get(t, "/screensaver"); !strings.Contains(body, "Late call") {
		t.Fatalf("with Night mode Off the screensaver doesn't show at night: %q", body)
	}
}

func TestBlackNightModeReleasesTheWakeLockBetweenItsHours(t *testing.T) {
	h := household(t)
	post(t, h, "/device/wall-display", url.Values{"wall_display": {"on"}})
	addEvent(t, h, timed("Dentist", "2026-03-15T15:00", "2026-03-15T23:30", "UTC"))
	nightMode(t, h, "black")
	if _, body := h.Get(t, "/settings"); !strings.Contains(body, `<option value="black" selected`) ||
		!strings.Contains(body, `name="night_start" value="22"`) || !strings.Contains(body, `name="night_end" value="6"`) {
		t.Fatalf("Settings doesn't show Night mode Black 22 to 6: %q", body)
	}

	// 09:00, before night: the ordinary screensaver.
	if _, body := h.Get(t, "/screensaver"); !strings.Contains(body, "Dentist") || !strings.Contains(body, holdsWakeLock) {
		t.Fatalf("by day the screensaver isn't shown: %q", body)
	}
	for _, at := range []int{22, 23, 0, 5} {
		h.Clock.Set(time.Date(2026, 3, 15, at, 0, 0, 0, time.UTC))
		_, body := h.Get(t, "/screensaver")
		if strings.Contains(body, "Dentist") || strings.Contains(body, "</time>") || strings.Contains(body, holdsWakeLock) {
			t.Fatalf("at %02d:00 Black Night mode shows something or holds the wake lock: %q", at, body)
		}
		if !strings.Contains(body, `<a id="wake" href="/"`) {
			t.Fatalf("at %02d:00 a touch doesn't wake to the Default view: %q", at, body)
		}
	}
	h.Clock.Set(time.Date(2026, 3, 16, 6, 0, 0, 0, time.UTC))
	if _, body := h.Get(t, "/screensaver"); !strings.Contains(body, holdsWakeLock) {
		t.Fatalf("at 06:00 Night mode didn't end: %q", body)
	}
}

// Night mode 22:00 to 06:00 in New York: the screensaver and Night pages
// reload when it next starts or ends, at its hours on either side of a DST
// change, and whenever the Household's settings change.
func TestNightModeTurnsOnItsHoursAcrossDST(t *testing.T) {
	h := household(t, apptest.WithHouseholdTimezone("America/New_York"))
	nightMode(t, h, "black")
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 3, 8, 6, 0, 0, 0, time.UTC), "14400"},   // 01:00 EST; clocks go forward at 02:00; ends 06:00 EDT
		{time.Date(2026, 3, 8, 12, 30, 0, 0, time.UTC), "48600"}, // 08:30 EDT; starts 22:00 EDT
		{time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC), "21600"},  // 01:00 EDT; clocks go back at 02:00; ends 06:00 EST
		{time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC), "43200"}, // 10:00 EST; starts 22:00 EST
	} {
		h.Clock.Set(c.at)
		_, body := h.Get(t, "/screensaver")
		if !strings.Contains(body, `data-reload-seconds="`+c.want+`"`) {
			t.Errorf("at %s the screensaver doesn't reload in %s s: %q", c.at, c.want, body)
		}
		if !strings.Contains(body, `addEventListener("sse:household", () => location.reload())`) {
			t.Errorf("at %s the screensaver doesn't reload on a household change", c.at)
		}
	}
}

func TestNightModeEndsInTheSpringForwardGapAndAcrossTheFold(t *testing.T) {
	h := household(t, apptest.WithHouseholdTimezone("America/New_York"))
	post(t, h, "/settings/household", url.Values{"default_view": {"today"}, "week_start": {"0"}, "clock_24h": {"on"},
		"night_mode": {"black"}, "night_start": {"23"}, "night_end": {"2"}})
	for _, c := range []struct {
		at   time.Time
		want string
	}{
		{time.Date(2026, 3, 8, 6, 0, 0, 0, time.UTC), "3600"},   // 01:00 EST; 02:00 is 03:00 EDT, Night mode ends then
		{time.Date(2026, 3, 8, 6, 30, 0, 0, time.UTC), "1800"},  // 01:30 EST
		{time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC), "70200"}, // 03:30 EDT, off; starts 23:00 EDT
		{time.Date(2026, 11, 1, 5, 0, 0, 0, time.UTC), "7200"},  // 01:00 EDT; 01:00 EST, then ends 02:00 EST
		{time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC), "1800"}, // 01:30 EST
	} {
		h.Clock.Set(c.at)
		if _, body := h.Get(t, "/screensaver"); !strings.Contains(body, `data-reload-seconds="`+c.want+`"`) {
			t.Errorf("at %s the screensaver doesn't reload in %s s", c.at, c.want)
		}
	}
}

func TestWithNightModeOffTheScreensaverNeverReloads(t *testing.T) {
	h := household(t)
	if _, body := h.Get(t, "/screensaver"); !strings.Contains(body, `data-reload-seconds="0"`) || strings.Contains(body, "60000") {
		t.Fatalf("with Night mode off the screensaver reloads or refetches by the minute: %q", body)
	}
}

func TestDimClockNightModeKeepsTheWakeLockAndShowsTheTime(t *testing.T) {
	h := household(t)
	post(t, h, "/device/wall-display", url.Values{"wall_display": {"on"}})
	addEvent(t, h, timed("Dentist", "2026-03-15T15:00", "2026-03-15T23:30", "UTC"))
	nightMode(t, h, "dim_clock")
	h.Clock.Set(time.Date(2026, 3, 15, 23, 5, 0, 0, time.UTC))
	_, body := h.Get(t, "/screensaver")
	for _, want := range []string{">23:05</time>", "Sunday 15 March", holdsWakeLock, `<a id="wake" href="/"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("Dim clock has no %q: %q", want, body)
		}
	}
	if strings.Contains(body, "Dentist") {
		t.Fatal("Dim clock shows the agenda")
	}
	// Midnight, 55 minutes off, turns its date before Night mode ends at 06:00.
	if !strings.Contains(body, `data-reload-seconds="3300"`) {
		t.Fatalf("Dim clock doesn't reload at midnight: %q", body)
	}
}

func TestNightModeSettingsAreValidated(t *testing.T) {
	h := household(t)
	for _, bad := range []url.Values{
		{"night_mode": {"black"}, "night_start": {"24"}, "night_end": {"6"}},
		{"night_mode": {"sleep"}, "night_start": {"22"}, "night_end": {"6"}},
	} {
		bad.Set("default_view", "today")
		bad.Set("week_start", "0")
		if code, _ := h.Post(t, "/settings/household", bad); code != http.StatusBadRequest {
			t.Fatalf("%v = %d, want 400", bad, code)
		}
	}
}

const watchesMotion = `getUserMedia`

func TestAnAdminTurnsOnMotionWakeWhichRunsOnlyOnTheScreensaverAndDimClock(t *testing.T) {
	h := household(t) // unlocked as Ada at 09:00
	post(t, h, "/device/wall-display", url.Values{"wall_display": {"on"}})
	if _, body := h.Get(t, "/settings"); !strings.Contains(body, `name="motion_wake"`) || strings.Contains(body, `name="motion_wake" checked`) {
		t.Fatalf("This device doesn't offer motion-wake, off by default: %q", body)
	}
	if !asksForPIN(t, h.Device(), "/device/motion-wake", url.Values{"motion_wake": {"on"}}) {
		t.Fatal("a locked device turned on motion-wake")
	}
	nightMode(t, h, "dim_clock")
	post(t, h, "/device/motion-wake", url.Values{"motion_wake": {"on"}})
	if _, body := h.Get(t, "/settings"); !strings.Contains(body, `name="motion_wake" checked`) || !strings.Contains(body, `id="motion-wake"`) {
		t.Fatalf("This device doesn't show motion-wake on with its camera status: %q", body)
	}
	if _, body := h.Get(t, "/today"); strings.Contains(body, watchesMotion) {
		t.Fatal("motion-wake runs during active use")
	}
	for _, at := range []int{9, 23} { // the screensaver, then Dim clock
		h.Clock.Set(time.Date(2026, 3, 15, at, 0, 0, 0, time.UTC))
		if _, body := h.Get(t, "/screensaver"); !strings.Contains(body, watchesMotion) || !strings.Contains(body, `id="camera"`) {
			t.Fatalf("at %02d:00 motion-wake doesn't run with its camera icon: %q", at, body)
		}
	}

	unlock(t, h, 1, "1234")
	nightMode(t, h, "black")
	if _, body := h.Get(t, "/screensaver"); strings.Contains(body, watchesMotion) {
		t.Fatal("motion-wake runs during Black Night mode")
	}

	h.Clock.Set(time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC))
	unlock(t, h, 1, "1234")
	post(t, h, "/device/wall-display", nil)
	if _, body := h.Get(t, "/screensaver"); strings.Contains(body, watchesMotion) {
		t.Fatal("motion-wake runs on a browser that isn't a Wall display")
	}
}
