package app_test

import (
	"net/url"
	"strings"
	"testing"
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
