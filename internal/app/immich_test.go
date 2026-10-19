package app_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/app"
	"github.com/DeRusto/goCasa/internal/apptest"
)

// fakeImmich is an Immich v3 server's albums, people, metadata search and
// thumbnails, kept in memory. Each asset's preview bytes are "img:<id>".
type fakeImmich struct {
	mu       sync.Mutex
	version  [3]int
	albums   [][2]string         // id, name
	people   [][2]string         // id, name
	assets   map[string][]string // album or person id → asset ids
	pageSize int
	down     bool
	searches []map[string]any // each search body
	thumbs   map[string]int   // asset id → preview fetches
	keys     []string         // each request's x-api-key
	*httptest.Server
}

const immichKey = "immich-key-123"

func newImmich(t *testing.T) *fakeImmich {
	f := &fakeImmich{version: [3]int{3, 3, 3}, assets: map[string][]string{}, thumbs: map[string]int{}, pageSize: 2}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeImmich) setDown(d bool) { f.mu.Lock(); f.down = d; f.mu.Unlock() }

func (f *fakeImmich) set(owner string, ids ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assets[owner] = ids
}

func (f *fakeImmich) fetches(id string) int { f.mu.Lock(); defer f.mu.Unlock(); return f.thumbs[id] }

func (f *fakeImmich) lastSearch() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.searches) == 0 {
		return nil
	}
	return f.searches[len(f.searches)-1]
}

func (f *fakeImmich) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	reply := func(v any) { json.NewEncoder(w).Encode(v) }
	if r.URL.Path == "/api/server/version" {
		reply(map[string]int{"major": f.version[0], "minor": f.version[1], "patch": f.version[2]})
		return
	}
	f.keys = append(f.keys, r.Header.Get("x-api-key"))
	if r.Header.Get("x-api-key") != immichKey {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch p := r.URL.Path; {
	case p == "/api/albums":
		out := []any{}
		for _, a := range f.albums {
			out = append(out, map[string]any{"id": a[0], "albumName": a[1]})
		}
		reply(out)
	case p == "/api/people":
		out := []any{}
		for _, a := range f.people {
			out = append(out, map[string]any{"id": a[0], "name": a[1], "isHidden": false})
		}
		out = append(out, map[string]any{"id": "unnamed", "name": "", "isHidden": false})
		reply(map[string]any{"people": out, "hasNextPage": false})
	case p == "/api/search/metadata" && r.Method == http.MethodPost:
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.searches = append(f.searches, body)
		var ids []string
		seen := map[string]bool{}
		filter, _ := body["filter"].(map[string]any)
		branches, _ := filter["or"].([]any)
		for _, b := range branches {
			for _, key := range []string{"albumIds", "personIds"} {
				m, _ := b.(map[string]any)[key].(map[string]any)
				anyOf, _ := m["any"].([]any)
				for _, owner := range anyOf {
					for _, id := range f.assets[owner.(string)] {
						if !seen[id] {
							seen[id] = true
							ids = append(ids, id)
						}
					}
				}
			}
		}
		start := 0
		if c, ok := body["cursor"].(string); ok {
			fmt.Sscan(c, &start)
		}
		end := min(start+f.pageSize, len(ids))
		var next any
		if end < len(ids) {
			next = fmt.Sprint(end)
		}
		items := []any{}
		for _, id := range ids[start:end] {
			items = append(items, map[string]any{"id": id, "type": "IMAGE", "fileCreatedAt": "2025-07-01T10:00:00.000Z"})
		}
		reply(map[string]any{"assets": map[string]any{"items": items, "count": len(items), "total": len(items), "nextCursor": next}})
	case strings.HasPrefix(p, "/api/assets/") && strings.HasSuffix(p, "/thumbnail"):
		id := strings.TrimSuffix(strings.TrimPrefix(p, "/api/assets/"), "/thumbnail")
		if r.URL.Query().Get("size") != "preview" {
			http.Error(w, "want size=preview", http.StatusBadRequest)
			return
		}
		f.thumbs[id]++
		w.Header().Set("Content-Type", "application/octet-stream")
		fmt.Fprint(w, "img:"+id)
	default:
		http.NotFound(w, r)
	}
}

// immichHousehold is a set-up, unlocked Household on a Wall display with
// Immich connected: album "Summer" holds a1, a2, a3; Ada is in a3 and p1.
func immichHousehold(t *testing.T) (*apptest.Harness, *fakeImmich) {
	t.Helper()
	f := newImmich(t)
	f.albums = [][2]string{{"alb-summer", "Summer"}, {"alb-pets", "Pets"}}
	f.people = [][2]string{{"per-ada", "Ada"}}
	f.set("alb-summer", "a1", "a2", "a3")
	f.set("per-ada", "a3", "p1")
	h := apptest.New(t)
	setup(t, h)
	if code, page := h.Post(t, "/settings/immich", url.Values{"url": {f.URL + "/"}, "key": {immichKey}}); code != http.StatusOK || !strings.Contains(page, "Summer") {
		t.Fatalf("connect Immich = %d %q", code, page)
	}
	return h, f
}

var photoRE = regexp.MustCompile(`<img id="photo" src="(/screensaver/photos/\d+)"`)

// photo is the screensaver's photo address on device h, or "" for none.
// Entering the screensaver ends an unlock, so tests look from another device.
func photo(t *testing.T, h *apptest.Harness) string {
	t.Helper()
	_, page := h.Get(t, "/screensaver")
	m := photoRE.FindStringSubmatch(page)
	if m == nil {
		return ""
	}
	return m[1]
}

func TestImmichPickedAlbumsAndPeopleRotateOnTheScreensaver(t *testing.T) {
	h, f := immichHousehold(t)
	wall := h.Device()
	if p := photo(t, wall); p != "" {
		t.Fatalf("nothing picked, yet the screensaver shows %s", p)
	}
	post(t, h, "/settings/immich/picks", url.Values{"album": {"alb-summer"}, "person": {"per-ada"}})

	body := f.lastSearch()
	b, _ := json.Marshal(body["filter"])
	for _, want := range []string{`"albumIds":{"any":["alb-summer"]}`, `"personIds":{"any":["per-ada"]}`, `"trashedAt":{"eq":null}`, `"visibility":{"eq":"timeline"}`, `"type":{"eq":"IMAGE"}`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("search filter %s has no %s", b, want)
		}
	}
	f.mu.Lock()
	for _, k := range f.keys {
		if k != immichKey {
			t.Errorf("x-api-key = %q", k)
		}
	}
	f.mu.Unlock()

	seen := map[string]bool{}
	for range 4 { // a1, a2, a3, p1 across two search pages
		p := photo(t, wall)
		if p == "" || seen[p] {
			t.Fatalf("photo %q after %v: missing or repeated", p, seen)
		}
		seen[p] = true
		code, img := wall.Get(t, p)
		if code != http.StatusOK || !strings.HasPrefix(img, "img:") {
			t.Fatalf("%s = %d %q", p, code, img)
		}
		h.Clock.Advance(30 * time.Second)
	}
}

func TestImmichRefusesOldVersionsAndRefusedKeys(t *testing.T) {
	f := newImmich(t)
	f.version = [3]int{3, 1, 9}
	h := apptest.New(t)
	setup(t, h)
	if code, page := h.Post(t, "/settings/immich", url.Values{"url": {f.URL}, "key": {immichKey}}); code != http.StatusBadRequest || !strings.Contains(page, "v3.2") {
		t.Errorf("Immich v3.1 = %d %q", code, page)
	}
	f.version = [3]int{3, 2, 0}
	if code, page := h.Post(t, "/settings/immich", url.Values{"url": {f.URL}, "key": {"wrong"}}); code != http.StatusBadRequest || !strings.Contains(page, "API key") {
		t.Errorf("wrong key = %d %q", code, page)
	}
	if n := count(t, h, "SELECT count(*) FROM integrations WHERE type = 'immich'"); n != 0 {
		t.Errorf("refused connects left %d integrations", n)
	}
	if _, page := h.Get(t, "/settings"); !strings.Contains(page, "Immich isn't connected") {
		t.Errorf("Settings doesn't say why no photos show: %q", page)
	}
	post(t, h, "/settings/immich", url.Values{"url": {f.URL}, "key": {immichKey}})
	var enc []byte
	if err := h.App.DB.QueryRow("SELECT credentials_enc FROM integrations WHERE type = 'immich'").Scan(&enc); err != nil || strings.Contains(string(enc), immichKey) {
		t.Errorf("API key stored in the clear or missing: %v", err)
	}
}

func TestImmichRelistsHourlyAndOnRefreshNow(t *testing.T) {
	h, f := immichHousehold(t)
	wall := h.Device()
	post(t, h, "/settings/immich/picks", url.Values{"album": {"alb-pets"}})
	if _, page := h.Get(t, "/settings"); !strings.Contains(page, "the picked albums and people have no photos") || !strings.Contains(page, `value="alb-pets" checked`) {
		t.Errorf("Settings doesn't say the picks are empty: %q", page)
	}
	f.set("alb-pets", "c1")
	h.Clock.Advance(59 * time.Minute)
	h.RunDue()
	if p := photo(t, wall); p != "" {
		t.Fatalf("re-listed before the hour: %s", p)
	}
	h.Clock.Advance(time.Minute)
	h.RunDue()
	p := photo(t, wall)
	if p == "" {
		t.Fatal("hourly re-list found nothing")
	}
	f.set("alb-pets", "c2")
	unlock(t, h, 1, "1234") // the hour's wait ended the unlock
	post(t, h, "/settings/immich/refresh", nil)
	h.Clock.Advance(30 * time.Second)
	if q := photo(t, wall); q == "" || q == p {
		t.Fatalf("after Refresh now the screensaver shows %q, was %q", q, p)
	}
	if code, img := wall.Get(t, p); code != http.StatusNotFound {
		t.Errorf("a photo no longer listed = %d %q", code, img)
	}
	if _, page := h.Get(t, "/settings"); !strings.Contains(page, "1 photos in the rotation") {
		t.Errorf("Settings doesn't count the photos: %q", page)
	}

	post(t, h, "/settings/immich/picks", nil)
	h.Clock.Advance(30 * time.Second)
	if q := photo(t, wall); q != "" {
		t.Fatalf("nothing picked, yet %s shows", q)
	}
	if _, page := h.Get(t, "/settings"); !strings.Contains(page, "pick at least one album or person") {
		t.Errorf("Settings doesn't say nothing is picked: %q", page)
	}
}

func TestEachPhotoShowsForSecondsPerPhotoWithoutRepeats(t *testing.T) {
	h, _ := immichHousehold(t)
	wall := h.Device()
	post(t, h, "/settings/immich/picks", url.Values{"album": {"alb-summer"}})
	post(t, h, "/settings/household", url.Values{"default_view": {"today"}, "week_start": {"0"}, "seconds_per_photo": {"60"}})
	if _, page := h.Get(t, "/settings"); !strings.Contains(page, `name="seconds_per_photo" value="60"`) {
		t.Errorf("Settings doesn't show seconds per photo")
	}
	first := photo(t, wall)
	h.Clock.Advance(59 * time.Second)
	if _, page := wall.Get(t, "/screensaver"); !strings.Contains(page, first+`"`) || !strings.Contains(page, `data-photo-seconds="1"`) {
		t.Fatalf("photo changed before 60 s or reloads at the wrong time: %q", page)
	}
	h.Clock.Advance(time.Second)
	second := photo(t, wall)
	h.Clock.Advance(time.Minute)
	third := photo(t, wall)
	if len(map[string]bool{first: true, second: true, third: true}) != 3 {
		t.Errorf("a round of three repeated a photo: %s %s %s", first, second, third)
	}
}

func fetchesOf(f *fakeImmich, ids ...string) int {
	n := 0
	for _, id := range ids {
		n += f.fetches(id)
	}
	return n
}

func TestPhotosAreCachedPrefetchedAndOutliveAnImmichOutage(t *testing.T) {
	h, f := immichHousehold(t)
	wall := h.Device()
	post(t, h, "/settings/immich/picks", url.Values{"album": {"alb-summer"}, "person": {"per-ada"}})
	all := []string{"a1", "a2", "a3", "p1"}
	first := photo(t, wall)
	wall.Get(t, first)
	wall.Get(t, first)
	if n := fetchesOf(f, all...); n != 1 {
		t.Fatalf("showing one photo twice fetched %d from Immich, want 1", n)
	}
	h.RunDue()
	if n := fetchesOf(f, all...); n != 4 {
		t.Fatalf("after prefetch %d fetched, want the showing one and the next 3", n)
	}

	f.setDown(true)
	h.Clock.Advance(time.Hour)
	h.RunDue() // the re-list fails, keeping the list
	unlock(t, h, 1, "1234")
	if _, page := h.Get(t, "/settings"); !strings.Contains(page, "Immich answered 502") {
		t.Errorf("Settings doesn't show the last error: %q", page)
	}
	seen := map[string]bool{}
	for range 4 {
		p := photo(t, wall)
		if code, img := wall.Get(t, p); code != http.StatusOK || !strings.HasPrefix(img, "img:") {
			t.Fatalf("cached %s while Immich is down = %d %q", p, code, img)
		}
		seen[p] = true
		h.Clock.Advance(30 * time.Second)
	}
	if len(seen) < 3 {
		t.Errorf("rotation while down showed only %v", seen)
	}
}

func TestThePhotoCacheDropsTheLeastRecentlyUsed(t *testing.T) {
	defer app.SetPhotoCacheLimit(2 * len("img:a1"))()
	h, f := immichHousehold(t)
	wall := h.Device()
	post(t, h, "/settings/immich/picks", url.Values{"album": {"alb-summer"}})
	var ps []string
	for range 3 {
		p := photo(t, wall)
		wall.Get(t, p)
		ps = append(ps, p)
		h.Clock.Advance(30 * time.Second)
	}
	if n := count(t, h, "SELECT count(*) FROM photos_cache WHERE cache_path IS NOT NULL"); n != 2 {
		t.Errorf("%d photos cached, want 2 at the limit", n)
	}
	before := fetchesOf(f, "a1", "a2", "a3")
	wall.Get(t, ps[2]) // most recent: still cached
	if fetchesOf(f, "a1", "a2", "a3") != before {
		t.Errorf("the most recent photo was evicted")
	}
	wall.Get(t, ps[0]) // least recent: fetched again
	if after := fetchesOf(f, "a1", "a2", "a3"); after != before+1 {
		t.Errorf("the least recent photo wasn't evicted: %d fetches, want %d", after, before+1)
	}
}

func TestScreensaverRotatesPhotosWithoutReloading(t *testing.T) {
	h, _ := immichHousehold(t)
	wall := h.Device()
	post(t, h, "/settings/immich/picks", url.Values{"album": {"alb-summer"}})
	first := photo(t, wall)
	var p struct {
		Src     string `json:"src"`
		Seconds int    `json:"seconds"`
	}
	_, body := wall.Get(t, "/screensaver/photo")
	if err := json.Unmarshal([]byte(body), &p); err != nil || p.Src != first || p.Seconds < 1 {
		t.Fatalf("current photo = %q (%v), want %s", body, err, first)
	}
	h.Clock.Advance(30 * time.Second)
	_, body = wall.Get(t, "/screensaver/photo")
	if err := json.Unmarshal([]byte(body), &p); err != nil || p.Src == first || p.Src == "" {
		t.Fatalf("next photo = %q, want a new one", body)
	}
	if _, page := wall.Get(t, "/screensaver"); !strings.Contains(page, `fetch("/screensaver/photo")`) {
		t.Fatal("the screensaver does not swap photos in place")
	}
}
