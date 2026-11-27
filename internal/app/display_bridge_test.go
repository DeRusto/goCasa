package app_test

import (
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

// bridgeTag finds the Display app Bridge's script tag and its screen and cameraWake.
var bridgeTag = regexp.MustCompile(`<script src="/static/bridge\.js\?v=[0-9a-f]+" data-screen="([a-z-]+)" data-camera-wake="(true|false)"></script>`)

func bridgeOn(t *testing.T, body string) (screen, cameraWake string) {
	t.Helper()
	m := bridgeTag.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("page doesn't load the Bridge: %q", body)
	}
	if strings.Index(body, m[0]) > strings.Index(body, "</head>") {
		t.Fatal("the Bridge loads after the page's own scripts, too late for them to see it")
	}
	return m[1], m[2]
}

func TestEveryPageTellsTheBridgeWhichScreenItShows(t *testing.T) {
	t.Parallel()
	h := household(t) // unlocked as Ada; Sunday 15 March 2026 09:00 UTC
	for _, page := range []string{"/today", "/calendar", "/settings"} {
		if screen, camera := bridgeOn(t, mustGet(t, h, page)); screen != "awake" || camera != "false" {
			t.Errorf("%s on a browser tells the Bridge %s, cameraWake %s", page, screen, camera)
		}
	}
	post(t, h, "/device/wall-display", url.Values{"wall_display": {"on"}})
	post(t, h, "/device/motion-wake", url.Values{"motion_wake": {"on"}})
	nightMode(t, h, "black")
	if screen, camera := bridgeOn(t, mustGet(t, h, "/today")); screen != "awake" || camera != "true" {
		t.Errorf("Today on a Wall display with Camera wake tells the Bridge %s, cameraWake %s", screen, camera)
	}
	if screen, camera := bridgeOn(t, mustGet(t, h, "/screensaver")); screen != "screensaver" || camera != "true" {
		t.Errorf("the screensaver tells the Bridge %s, cameraWake %s", screen, camera)
	}
	h.Clock.Set(time.Date(2026, 3, 15, 23, 0, 0, 0, time.UTC))
	if screen, camera := bridgeOn(t, mustGet(t, h, "/screensaver")); screen != "night-black" || camera != "true" {
		t.Errorf("Black Night mode tells the Bridge %s, cameraWake %s", screen, camera)
	}

	h.Clock.Set(time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC))
	unlock(t, h, 1, "1234")
	nightMode(t, h, "dim_clock")
	post(t, h, "/device/motion-wake", nil)
	h.Clock.Set(time.Date(2026, 3, 16, 23, 0, 0, 0, time.UTC))
	if screen, camera := bridgeOn(t, mustGet(t, h, "/screensaver")); screen != "night-dim" || camera != "false" {
		t.Errorf("Dim clock with Camera wake off tells the Bridge %s, cameraWake %s", screen, camera)
	}
}

func mustGet(t *testing.T, h interface {
	Get(testing.TB, string) (int, string)
}, path string) string {
	t.Helper()
	code, body := h.Get(t, path)
	if code != 200 {
		t.Fatalf("GET %s = %d", path, code)
	}
	return body
}
