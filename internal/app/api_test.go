package app_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/apptest"
)

// native calls the JSON API as the Display app does: no Origin or
// Sec-Fetch-Site, and its own cookie jar (h's). It returns the status, the
// body and whether the response set the unlock cookie.
func native(t *testing.T, h *apptest.Harness, method, path, body string) (int, string, bool) {
	t.Helper()
	req, err := http.NewRequest(method, h.Server.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := h.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	unlockSet := false
	for _, c := range res.Cookies() {
		if c.Name == "gocasa_unlock" {
			unlockSet = true
		}
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, string(b), unlockSet
}

// pinCheck posts {"memberId": id, "pin": pin} to /api/v1/pin and returns its result.
func pinCheck(t *testing.T, h *apptest.Harness, id int, pin string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"memberId": id, "pin": pin})
	code, res, unlockSet := native(t, h, http.MethodPost, "/api/v1/pin", string(body))
	if code != http.StatusOK {
		t.Fatalf("POST /api/v1/pin = %d %q", code, res)
	}
	if unlockSet {
		t.Fatalf("POST /api/v1/pin set the unlock cookie (PIN %s for %d)", pin, id)
	}
	var got struct{ Result string }
	if err := json.Unmarshal([]byte(res), &got); err != nil {
		t.Fatalf("POST /api/v1/pin body %q: %v", res, err)
	}
	return got.Result
}

func TestAPIMembersListsActiveMembersWhoArentPets(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)                                                                                                          // Ada, Admin, 1
	h.Post(t, "/settings/members", member("Mia", "#f4a261", "member", ""))                                               // 2
	h.Post(t, "/settings/members", url.Values{"name": {"Rex"}, "color": {"#22c55e"}, "role": {"member"}, "pet": {"on"}}) // 3
	h.Post(t, "/settings/members", member("Cy", "#ef4444", "member", ""))                                                // 4
	h.Post(t, "/settings/members/4/archive", nil)
	if code := uploadAvatar(t, h, "2", redPNG(t)); code != http.StatusOK {
		t.Fatalf("avatar upload = %d", code)
	}

	code, body, unlockSet := native(t, h.Device(), http.MethodGet, "/api/v1/members", "")
	if code != http.StatusOK || unlockSet {
		t.Fatalf("GET /api/v1/members = %d (unlock cookie set: %v) %q", code, unlockSet, body)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	want := `[{"admin":true,"avatarUrl":null,"color":"#3b82f6","id":1,"name":"Ada"},{"admin":false,"avatarUrl":"/members/2/avatar","color":"#f4a261","id":2,"name":"Mia"}]`
	if norm, _ := json.Marshal(got); string(norm) != want {
		t.Fatalf("members = %s\nwant      %s", norm, want)
	}
}

func TestAPIMembersIsAnEmptyListBeforeSetup(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	if code, body, _ := native(t, h, http.MethodGet, "/api/v1/members", ""); code != http.StatusOK || strings.TrimSpace(body) != "[]" {
		t.Fatalf("GET /api/v1/members = %d %q, want []", code, body)
	}
}

func redPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestAPIPINAnswersYesNoOrBlockedAndNeverUnlocks(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)                                                               // Ada, Admin, PIN 1234, 1
	h.Post(t, "/settings/members", member("Mia", "#f4a261", "member", ""))    // 2
	h.Post(t, "/settings/members", member("Bob", "#22c55e", "admin", "5678")) // 3
	h.Post(t, "/settings/members/3/archive", nil)

	app := h.Device()
	if got := pinCheck(t, app, 1, "1234"); got != "yes" {
		t.Fatalf("right PIN = %q, want yes", got)
	}
	if _, body := app.Get(t, "/settings"); !strings.Contains(body, lockPage) {
		t.Fatal("a yes from the API unlocked the device")
	}
	for _, c := range []struct {
		id  int
		pin string
		why string
	}{
		{1, "0000", "wrong PIN"},
		{2, "1234", "not an Admin"},
		{3, "5678", "archived Admin"},
		{99, "1234", "unknown id"},
	} {
		if got := pinCheck(t, app, c.id, c.pin); got != "no" {
			t.Errorf("%s = %q, want no", c.why, got)
		}
	}
	for i := 0; i < 3; i++ {
		pinCheck(t, app, 1, "0000") // with the one above: four wrong
	}
	if got := pinCheck(t, app, 1, "0000"); got != "blocked" {
		t.Fatalf("fifth wrong PIN = %q, want blocked", got)
	}
	if got := pinCheck(t, app, 1, "1234"); got != "blocked" {
		t.Fatalf("right PIN while blocked = %q, want blocked", got)
	}
	h.Clock.Advance(5 * time.Minute)
	if got := pinCheck(t, app, 1, "1234"); got != "yes" {
		t.Fatalf("right PIN after the block = %q, want yes", got)
	}
}

func TestAPIPINSetsNoUnlockCookieOnAnUnlockedDevice(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h) // h's device is unlocked as Ada
	pinCheck(t, h, 1, "1234")
	pinCheck(t, h, 1, "0000")
	if _, _, unlockSet := native(t, h, http.MethodGet, "/api/v1/members", ""); unlockSet {
		t.Fatal("GET /api/v1/members set the unlock cookie")
	}
}

func TestAPIPINSharesTheBlockWithTheLockPage(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)

	// Wrong tries on /lock count towards the API's block...
	for i := 0; i < 4; i++ {
		unlock(t, h.Device(), 1, "0000")
	}
	if got := pinCheck(t, h.Device(), 1, "0000"); got != "blocked" {
		t.Fatalf("API try after four wrong on /lock = %q, want blocked", got)
	}
	if code, _ := unlock(t, h.Device(), 1, "1234"); code != http.StatusTooManyRequests {
		t.Fatalf("/lock while the API blocked it = %d, want 429", code)
	}

	// ...and the API's towards /lock's.
	h.Clock.Advance(5 * time.Minute)
	for i := 0; i < 4; i++ {
		pinCheck(t, h.Device(), 1, "0000")
	}
	if code, body := unlock(t, h.Device(), 1, "0000"); code != http.StatusTooManyRequests || !strings.Contains(body, "blocked for 5 minutes") {
		t.Fatalf("/lock try after four wrong on the API = %d", code)
	}
	if got := pinCheck(t, h.Device(), 1, "1234"); got != "blocked" {
		t.Fatalf("API while /lock blocked it = %q, want blocked", got)
	}
}

func TestAPIPINRefusesABodyThatIsntItsJSON(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)
	for _, body := range []string{
		``,
		`not json`,
		`[]`,
		`{"memberId": "1", "pin": "1234"}`,
		`{"memberId": 1, "pin": 1234}`,
		`{"memberId": 1.5, "pin": "1234"}`,
		`{"pin": "1234"}`,
		`{"memberId": 1}`,
		`{"memberId": 1, "pin": "1234", "extra": true}`,
		`{"memberId": 1, "pin": "1234"} {}`,
	} {
		if code, res, _ := native(t, h.Device(), http.MethodPost, "/api/v1/pin", body); code != http.StatusBadRequest {
			t.Errorf("body %q = %d %q, want 400", body, code, res)
		}
	}
	if n := count(t, h, "SELECT pin_failures FROM members WHERE id = 1"); n != 0 {
		t.Fatalf("a malformed body counted as %d wrong tries", n)
	}
}

func TestAPIPINStillRefusesACrossSiteBrowser(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)
	req, _ := http.NewRequest(http.MethodPost, h.Server.URL+"/api/v1/pin", strings.NewReader(`{"memberId": 1, "pin": "0000"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	res, err := h.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site POST /api/v1/pin = %d, want 403", res.StatusCode)
	}
	if n := count(t, h, "SELECT pin_failures FROM members WHERE id = 1"); n != 0 {
		t.Fatal("a cross-site try counted")
	}
}
