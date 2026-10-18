package app_test

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/app"
	"github.com/DeRusto/goCasa/internal/apptest"
)

const lockPage = "Enter your PIN"

func member(name, color, role, pin string) url.Values {
	return url.Values{"name": {name}, "color": {color}, "role": {role}, "pin": {pin}}
}

// setup runs first-run setup on h's device, leaving it unlocked as Ada (id 1).
func setup(t *testing.T, h *apptest.Harness) {
	t.Helper()
	if code, body := h.Post(t, "/setup", member("Ada", "#3b82f6", "", "1234")); code != http.StatusOK || !strings.Contains(body, "<h1>Settings</h1>") {
		t.Fatalf("setup = %d %q", code, body)
	}
}

func unlock(t *testing.T, h *apptest.Harness, id int64, pin string) (int, string) {
	t.Helper()
	return h.Post(t, "/lock", url.Values{"member_id": {strconv.FormatInt(id, 10)}, "pin": {pin}})
}

func adminCount(t *testing.T, h *apptest.Harness) int {
	t.Helper()
	var n int
	if err := h.App.DB.QueryRow("SELECT count(*) FROM members WHERE role = 'admin'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFirstRunCreatesAdminThenSettingsNeedsPIN(t *testing.T) {
	h := apptest.New(t)
	if _, body := h.Get(t, "/settings"); !strings.Contains(body, "Add the first Admin") {
		t.Fatalf("first run did not offer setup: %q", body)
	}
	if code, _ := h.Post(t, "/setup", member("Ada", "#3b82f6", "", "")); code != http.StatusBadRequest {
		t.Fatalf("setup without PIN = %d, want 400", code)
	}
	setup(t, h)

	// Setup is closed once a Member exists.
	other := h.Device()
	other.Post(t, "/setup", member("Eve", "#ff0000", "", "9999"))
	if adminCount(t, h) != 1 {
		t.Fatal("second setup created another Admin")
	}
	if _, body := other.Get(t, "/settings"); !strings.Contains(body, lockPage) || strings.Contains(body, "<h1>Settings</h1>") {
		t.Fatalf("locked device reached Settings: %q", body)
	}
	if code, _ := other.Post(t, "/settings/members", member("Eve", "#ff0000", "member", "")); code != http.StatusOK || adminCount(t, h) != 1 {
		t.Fatal("locked device changed Members")
	}
	var n int
	h.App.DB.QueryRow("SELECT count(*) FROM members").Scan(&n)
	if n != 1 {
		t.Fatalf("members = %d, want 1", n)
	}
	if code, _ := unlock(t, other, 1, "0000"); code != http.StatusUnauthorized {
		t.Fatalf("wrong PIN = %d, want 401", code)
	}
	if _, body := unlock(t, other, 1, "1234"); !strings.Contains(body, "<h1>Settings</h1>") {
		t.Fatalf("right PIN did not unlock: %q", body)
	}
}

func TestOnlyAdminsHavePINs(t *testing.T) {
	h := apptest.New(t)
	setup(t, h)
	if code, _ := h.Post(t, "/settings/members", member("Rex", "#22c55e", "member", "1111")); code != http.StatusBadRequest {
		t.Fatalf("Member with PIN = %d, want 400", code)
	}
	if code, _ := h.Post(t, "/settings/members", member("Bob", "#22c55e", "admin", "")); code != http.StatusBadRequest {
		t.Fatalf("Admin without PIN = %d, want 400", code)
	}
	h.Post(t, "/settings/members", member("Rex", "#22c55e", "member", "")) // id 2
	if code, _ := h.Post(t, "/settings/members/2", member("Rex", "#22c55e", "admin", "")); code != http.StatusBadRequest {
		t.Fatalf("promote without PIN = %d, want 400", code)
	}
	h.Post(t, "/settings/members/2", member("Rex", "#22c55e", "admin", "4321"))
	if adminCount(t, h) != 2 {
		t.Fatal("promotion with PIN did not happen")
	}
	if code, _ := unlock(t, h.Device(), 2, "4321"); code != http.StatusOK {
		t.Fatalf("new Admin's PIN = %d", code)
	}
	h.Post(t, "/settings/members/2", member("Rex", "#22c55e", "member", ""))
	var hasPIN bool
	h.App.DB.QueryRow("SELECT pin_hash IS NOT NULL FROM members WHERE id = 2").Scan(&hasPIN)
	if hasPIN {
		t.Fatal("demoted Member kept their PIN")
	}
	if code, _ := unlock(t, h.Device(), 2, "4321"); code != http.StatusUnauthorized {
		t.Fatalf("demoted Member's PIN = %d, want 401", code)
	}
}

func TestRefusesToDeleteOrDemoteTheLastAdmin(t *testing.T) {
	h := apptest.New(t)
	setup(t, h)
	if code, body := h.Post(t, "/settings/members/1/delete", nil); code != http.StatusConflict || !strings.Contains(body, "at least one Admin") {
		t.Fatalf("delete last Admin = %d %q", code, body)
	}
	if code, _ := h.Post(t, "/settings/members/1", member("Ada", "#3b82f6", "member", "")); code != http.StatusConflict || adminCount(t, h) != 1 {
		t.Fatalf("demote last Admin = %d", code)
	}
	h.Post(t, "/settings/members", member("Bob", "#22c55e", "admin", "5678"))
	if code, _ := h.Post(t, "/settings/members/1/delete", nil); code != http.StatusOK || adminCount(t, h) != 1 {
		t.Fatalf("delete one of two Admins = %d", code)
	}
}

func TestUnlockLastsFiveIdleMinutesOnThatDeviceOnly(t *testing.T) {
	h := apptest.New(t)
	setup(t, h)
	other := h.Device()
	if _, body := other.Get(t, "/settings"); !strings.Contains(body, lockPage) {
		t.Fatal("unlock leaked to another device")
	}

	h.Clock.Advance(4 * time.Minute)
	h.Get(t, "/settings") // activity resets the idle timer
	h.Clock.Advance(4*time.Minute + 59*time.Second)
	if _, body := h.Get(t, "/settings"); !strings.Contains(body, "<h1>Settings</h1>") {
		t.Fatal("locked before 5 idle minutes")
	}
	h.Clock.Advance(5 * time.Minute)
	if _, body := h.Get(t, "/settings"); !strings.Contains(body, lockPage) {
		t.Fatal("still unlocked after 5 idle minutes")
	}

	unlock(t, h, 1, "1234")
	h.Post(t, "/lock/now", nil)
	if _, body := h.Get(t, "/settings"); !strings.Contains(body, lockPage) {
		t.Fatal("still unlocked after Lock now")
	}
}

func TestUnlockCookieFlags(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		h := apptest.New(t, func(o *app.Options) { o.Config.BaseURL.Scheme = scheme })
		h.Client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		req, _ := http.NewRequest(http.MethodPost, h.Server.URL+"/setup", strings.NewReader(member("Ada", "#3b82f6", "", "1234").Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		res, err := h.Client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		cs := res.Cookies()
		if len(cs) != 1 || !cs[0].HttpOnly || cs[0].SameSite != http.SameSiteLaxMode || cs[0].Secure != (scheme == "https") {
			t.Fatalf("%s: cookies = %+v", scheme, cs)
		}
	}
}

func TestCrossSiteFormsAreRefused(t *testing.T) {
	h := apptest.New(t)
	req, _ := http.NewRequest(http.MethodPost, h.Server.URL+"/setup", strings.NewReader(member("Eve", "#ff0000", "", "1234").Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	res, err := h.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden || adminCount(t, h) != 0 {
		t.Fatalf("cross-site setup = %d", res.StatusCode)
	}
}
