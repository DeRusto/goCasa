package app_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
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
	albums   [][2]string               // id, name
	people   [][2]string               // id, name
	assets   map[string][]string       // album or person id → asset ids
	meta     map[string]map[string]any // asset id → extra fields it's listed with
	memories []string                  // today's Memories' asset ids
	noMemory bool                      // the key lacks memory.read
	pageSize int
	down     bool
	searches []map[string]any // each search body
	thumbs   map[string]int   // asset id → preview fetches
	keys     []string         // each request's x-api-key
	*httptest.Server
}

const immichKey = "immich-key-123"

func newImmich(t *testing.T) *fakeImmich {
	f := &fakeImmich{version: [3]int{3, 3, 3}, assets: map[string][]string{}, meta: map[string]map[string]any{}, thumbs: map[string]int{}, pageSize: 2}
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

func (f *fakeImmich) setMeta(id string, m map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.meta[id] = m
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
	asset := func(id string) map[string]any {
		m := map[string]any{"id": id, "type": "IMAGE", "fileCreatedAt": "2025-07-01T10:00:00.000Z"}
		for k, v := range f.meta[id] {
			m[k] = v
		}
		return m
	}
	switch p := r.URL.Path; {
	case p == "/api/memories":
		if f.noMemory {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if _, err := time.Parse(time.RFC3339, r.URL.Query().Get("for")); err != nil {
			http.Error(w, "want for=<date>", http.StatusBadRequest)
			return
		}
		items := []any{}
		for _, id := range f.memories {
			items = append(items, asset(id))
		}
		reply([]any{map[string]any{"id": "mem-1", "assets": items}})
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
		for _, key := range []string{"albumIds", "personIds"} { // a top-level none excludes
			m, _ := filter[key].(map[string]any)
			none, _ := m["none"].([]any)
			for _, owner := range none {
				for _, id := range f.assets[owner.(string)] {
					ids = slices.DeleteFunc(ids, func(x string) bool { return x == id })
				}
			}
		}
		if fav, _ := filter["isFavorite"].(map[string]any); fav != nil {
			ids = slices.DeleteFunc(ids, func(id string) bool { return f.meta[id]["isFavorite"] != true })
		}
		if r, _ := filter["rating"].(map[string]any); r != nil {
			ids = slices.DeleteFunc(ids, func(id string) bool {
				rating, _ := f.meta[id]["exifInfo"].(map[string]any)["rating"].(int)
				return rating < int(r["gte"].(float64))
			})
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
			items = append(items, asset(id))
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

var photoRE = regexp.MustCompile(`<img class="photo" src="(/screensaver/photos/\d+)"`)

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
	t.Parallel()
	h, f := immichHousehold(t)
	wall := h.Device()
	if p := photo(t, wall); p != "" {
		t.Fatalf("nothing picked, yet the screensaver shows %s", p)
	}
	post(t, h, "/settings/immich/picks", url.Values{"album:alb-summer": {"show"}, "person:per-ada": {"show"}})

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
	t.Parallel()
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
	t.Parallel()
	h, f := immichHousehold(t)
	wall := h.Device()
	post(t, h, "/settings/immich/picks", url.Values{"album:alb-pets": {"show"}})
	if _, page := h.Get(t, "/settings"); !strings.Contains(page, "the shown albums and people have no photos") || !strings.Contains(page, `name="album:alb-pets" value="show" checked`) {
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
	t.Parallel()
	h, _ := immichHousehold(t)
	wall := h.Device()
	post(t, h, "/settings/immich/picks", url.Values{"album:alb-summer": {"show"}})
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
	t.Parallel()
	h, f := immichHousehold(t)
	wall := h.Device()
	post(t, h, "/settings/immich/picks", url.Values{"album:alb-summer": {"show"}, "person:per-ada": {"show"}})
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

func TestAnOutageWithAPartialCacheRotatesOnlyCachedPhotos(t *testing.T) {
	t.Parallel()
	h, f := immichHousehold(t)
	wall := h.Device()
	post(t, h, "/settings/immich/picks", url.Values{"album:alb-summer": {"show"}, "person:per-ada": {"show"}})
	wall.Get(t, photo(t, wall)) // one cached, no prefetch yet
	f.setDown(true)
	h.Clock.Advance(time.Hour)
	h.RunDue() // the re-list and prefetch fail
	for range 4 {
		p := photo(t, wall)
		if code, img := wall.Get(t, p); code != http.StatusOK || !strings.HasPrefix(img, "img:") {
			t.Fatalf("while Immich is down the screensaver picked uncached %s = %d %q", p, code, img)
		}
		h.Clock.Advance(30 * time.Second)
	}
}

func TestThePhotoCacheDropsTheLeastRecentlyUsed(t *testing.T) {
	defer app.SetPhotoCacheLimit(2 * len("img:a1"))()
	h, f := immichHousehold(t)
	wall := h.Device()
	post(t, h, "/settings/immich/picks", url.Values{"album:alb-summer": {"show"}})
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
	t.Parallel()
	h, _ := immichHousehold(t)
	wall := h.Device()
	post(t, h, "/settings/immich/picks", url.Values{"album:alb-summer": {"show"}})
	first := photo(t, wall)
	var p struct {
		Photos []struct {
			Src string `json:"src"`
		} `json:"photos"`
		Seconds int `json:"seconds"`
	}
	_, body := wall.Get(t, "/screensaver/photo")
	if err := json.Unmarshal([]byte(body), &p); err != nil || len(p.Photos) != 1 || p.Photos[0].Src != first || p.Seconds < 1 {
		t.Fatalf("current photo = %q (%v), want %s", body, err, first)
	}
	h.Clock.Advance(30 * time.Second)
	_, body = wall.Get(t, "/screensaver/photo")
	if err := json.Unmarshal([]byte(body), &p); err != nil || len(p.Photos) != 1 || p.Photos[0].Src == first {
		t.Fatalf("next photo = %q, want a new one", body)
	}
	if _, page := wall.Get(t, "/screensaver"); !strings.Contains(page, `fetch("/screensaver/photo"`) {
		t.Fatal("the screensaver does not swap photos in place")
	}
}

type stepPhoto struct {
	Src     string `json:"src"`
	Caption string `json:"caption"`
}

// step is the photos /screensaver/photo shows now on device h, with query q.
func step(t *testing.T, h *apptest.Harness, q string) []stepPhoto {
	t.Helper()
	var p struct {
		Photos []stepPhoto `json:"photos"`
	}
	if _, body := h.Get(t, "/screensaver/photo"+q); json.Unmarshal([]byte(body), &p) != nil {
		t.Fatalf("/screensaver/photo%s = %q", q, body)
	}
	return p.Photos
}

// shown is the Immich asset ids the screensaver on wall shows over n steps.
func shown(t *testing.T, h, wall *apptest.Harness, n int) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for range n {
		for _, p := range step(t, wall, "") {
			_, img := wall.Get(t, p.Src)
			out[strings.TrimPrefix(img, "img:")] = true
		}
		h.Clock.Advance(30 * time.Second)
	}
	return out
}

func TestImmichFavoritesRatingAndExclusionsNarrowThePicks(t *testing.T) {
	t.Parallel()
	h, f := immichHousehold(t)
	wall := h.Device()
	f.setMeta("a1", map[string]any{"isFavorite": true, "exifInfo": map[string]any{"rating": 5}})
	f.setMeta("a2", map[string]any{"isFavorite": true, "exifInfo": map[string]any{"rating": 2}})
	f.setMeta("p1", map[string]any{"isFavorite": true, "exifInfo": map[string]any{"rating": 4}})

	post(t, h, "/settings/immich/picks", url.Values{"album:alb-summer": {"show"}, "person:per-ada": {"hide"}})
	b, _ := json.Marshal(f.lastSearch())
	for _, want := range []string{`"personIds":{"none":["per-ada"]}`, `"withExif":true`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("search %s has no %s", b, want)
		}
	}
	if strings.Contains(string(b), "isFavorite") || strings.Contains(string(b), "rating") {
		t.Errorf("search %s filters favorites or rating, which are off", b)
	}
	if got := shown(t, h, wall, 4); !maps.Equal(got, map[string]bool{"a1": true, "a2": true}) {
		t.Errorf("excluding Ada showed %v, want a1 and a2", got) // a3 is in Summer but shows Ada
	}

	post(t, h, "/settings/immich/picks", url.Values{"album:alb-summer": {"show"}, "album:alb-pets": {"hide"}, "person:per-ada": {"show"}, "favorites": {"on"}, "min_rating": {"3"}})
	b, _ = json.Marshal(f.lastSearch())
	for _, want := range []string{`"albumIds":{"none":["alb-pets"]}`, `"isFavorite":{"eq":true}`, `"rating":{"gte":3}`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("search %s has no %s", b, want)
		}
	}
	if got := shown(t, h, wall, 4); !maps.Equal(got, map[string]bool{"a1": true, "p1": true}) {
		t.Errorf("favorites rated 3+ showed %v, want a1 and p1", got)
	}
	if _, page := h.Get(t, "/settings"); !strings.Contains(page, `name="favorites" checked`) || !strings.Contains(page, `<option value="3" selected`) || !strings.Contains(page, `name="album:alb-pets" value="hide" checked`) {
		t.Errorf("Settings doesn't show the filters: %q", page)
	}

	post(t, h, "/settings/immich/picks", url.Values{"favorites": {"on"}})
	if got := shown(t, h, wall, 2); len(got) != 0 {
		t.Errorf("favorites only with nothing picked showed %v", got)
	}
	if code, _ := h.Post(t, "/settings/immich/picks", url.Values{"min_rating": {"6"}}); code != http.StatusBadRequest {
		t.Errorf("a 6-star minimum = %d", code)
	}
}

func TestTodaysImmichMemoriesJoinTheRotation(t *testing.T) {
	t.Parallel()
	h, f := immichHousehold(t)
	wall := h.Device()
	f.mu.Lock()
	f.memories = []string{"m1", "a1", "m2"}
	f.meta["m2"] = map[string]any{"type": "VIDEO"}
	f.mu.Unlock()
	post(t, h, "/settings/immich/picks", url.Values{"memories": {"on"}, "person:per-ada": {"show"}})
	if got := shown(t, h, wall, 4); !maps.Equal(got, map[string]bool{"m1": true, "a1": true, "a3": true, "p1": true}) {
		t.Errorf("Memories with Ada showed %v", got)
	}
	f.setMeta("p1", map[string]any{"isFavorite": true, "exifInfo": map[string]any{"rating": 5}})
	post(t, h, "/settings/immich/picks", url.Values{"memories": {"on"}, "person:per-ada": {"show"}, "favorites": {"on"}, "min_rating": {"4"}})
	if got := shown(t, h, wall, 3); !maps.Equal(got, map[string]bool{"m1": true, "a1": true, "p1": true}) {
		t.Errorf("Memories with Ada's 4-star favorites showed %v, want every Memory and p1", got) // the filters narrow picks, never Memories
	}
	post(t, h, "/settings/immich/picks", url.Values{"memories": {"on"}, "person:per-ada": {"show"}})

	f.mu.Lock()
	f.noMemory = true
	f.mu.Unlock()
	post(t, h, "/settings/immich/refresh", nil)
	if got := shown(t, h, wall, 2); !maps.Equal(got, map[string]bool{"a3": true, "p1": true}) {
		t.Errorf("with memory.read refused showed %v, want Ada's photos", got)
	}
	if _, page := h.Get(t, "/settings"); !strings.Contains(page, "memory.read permission") || strings.Contains(page, "Immich refused the API key:") {
		t.Errorf("Settings doesn't say Memories need memory.read, or shows the sync failing: %q", page)
	}
}

func TestImmichNeedsMemoryReadToConnect(t *testing.T) {
	t.Parallel()
	f := newImmich(t)
	f.noMemory = true
	h := apptest.New(t)
	setup(t, h)
	if code, page := h.Post(t, "/settings/immich", url.Values{"url": {f.URL}, "key": {immichKey}}); code != http.StatusBadRequest || !strings.Contains(page, "memory.read") {
		t.Errorf("a key without memory.read = %d %q", code, page)
	}
}

func TestEachPhotoHasACaptionOfItsDateAndPlace(t *testing.T) {
	t.Parallel()
	h, f := immichHousehold(t)
	wall := h.Device()
	f.setMeta("a1", map[string]any{"localDateTime": "2019-06-30T23:30:00.000Z", "fileCreatedAt": "2019-07-01T06:30:00.000Z", "exifInfo": map[string]any{"city": "Lisbon", "state": "Lisboa", "country": "Portugal"}})
	f.setMeta("a2", map[string]any{"localDateTime": "2021-03-02T10:00:00.000Z", "exifInfo": map[string]any{"state": "Bavaria", "country": "Germany"}})
	f.setMeta("a3", map[string]any{"localDateTime": "2022-12-25T10:00:00.000Z", "exifInfo": map[string]any{}})
	post(t, h, "/settings/immich/picks", url.Values{"album:alb-summer": {"show"}})
	captions := map[string]bool{}
	for range 3 {
		for _, p := range step(t, wall, "") {
			captions[p.Caption] = true
		}
		h.Clock.Advance(30 * time.Second)
	}
	want := map[string]bool{"June 2019 · Lisbon, Portugal": true, "March 2021 · Bavaria, Germany": true, "December 2022": true}
	if !maps.Equal(captions, want) {
		t.Errorf("captions %v, want %v", captions, want)
	}
	if _, page := wall.Get(t, "/screensaver"); !strings.Contains(page, `class="caption"`) {
		t.Errorf("the screensaver shows no caption: %q", page)
	}

	post(t, h, "/settings/household", url.Values{"default_view": {"today"}, "week_start": {"0"}, "photo_captions": {"off"}})
	if ps := step(t, wall, ""); len(ps) != 1 || ps[0].Caption != "" {
		t.Errorf("captions off still shows %v", ps)
	}
}

func TestPortraitPhotosShowInPairsOnLandscapeScreens(t *testing.T) {
	t.Parallel()
	h, f := immichHousehold(t)
	wall := h.Device()
	f.set("alb-summer", "l1", "t1", "t2", "t3", "t4", "s1")
	f.setMeta("l1", map[string]any{"width": 4000, "height": 3000})
	for _, id := range []string{"t1", "t2", "t3"} {
		f.setMeta(id, map[string]any{"width": 3000, "height": 4000})
	}
	f.setMeta("t4", map[string]any{"exifInfo": map[string]any{"exifImageWidth": 4000, "exifImageHeight": 3000, "orientation": "6"}}) // stored sideways
	post(t, h, "/settings/immich/picks", url.Values{"album:alb-summer": {"show"}})
	portraits := map[string]bool{"t1": true, "t2": true, "t3": true, "t4": true}
	seen := map[string]bool{}
	for range 4 { // l1, s1 and two pairs
		ps := step(t, wall, "")
		for _, p := range ps {
			_, img := wall.Get(t, p.Src)
			id := strings.TrimPrefix(img, "img:")
			if seen[id] {
				t.Fatalf("%s repeated in a round", id)
			}
			seen[id] = true
			if portraits[id] != (len(ps) == 2) {
				t.Errorf("step %v: portraits pair, landscapes show alone", ps)
			}
		}
		if len(ps) != 2 {
			h.Clock.Advance(30 * time.Second)
			continue
		}
		if p := step(t, wall, "?portrait=1"); len(p) != 1 || p[0].Src != ps[0].Src {
			t.Errorf("a portrait screen's first half shows %v, want %s", p, ps[0].Src)
		}
		h.Clock.Advance(15 * time.Second)
		if p := step(t, wall, "?portrait=1"); len(p) != 1 || p[0].Src != ps[1].Src {
			t.Errorf("a portrait screen's second half shows %v, want %s", p, ps[1].Src)
		}
		h.Clock.Advance(15 * time.Second)
	}
	if len(seen) != 6 {
		t.Errorf("a round showed %v, want all six", seen)
	}
}

func TestScreensaverPhotoFitAndZoomSettings(t *testing.T) {
	t.Parallel()
	h, _ := immichHousehold(t)
	wall := h.Device()
	post(t, h, "/settings/immich/picks", url.Values{"album:alb-summer": {"show"}})
	if _, page := wall.Get(t, "/screensaver"); !strings.Contains(page, `data-fit="fill" data-zoom`) || strings.Contains(page, `class="bg"`) {
		t.Errorf("by default photos fill the screen and zoom: %q", page)
	}
	post(t, h, "/settings/household", url.Values{"default_view": {"today"}, "week_start": {"0"}, "photo_fit": {"blur"}, "photo_zoom": {"off"}})
	if _, page := wall.Get(t, "/screensaver"); !strings.Contains(page, `data-fit="blur">`) || !strings.Contains(page, `class="bg"`) {
		t.Errorf("Fit with blurred background, zoom off: %q", page)
	}
	if _, page := h.Get(t, "/settings"); !strings.Contains(page, `<option value="blur" selected`) {
		t.Errorf("Settings doesn't show the fit")
	}
	if code, _ := h.Post(t, "/settings/household", url.Values{"default_view": {"today"}, "week_start": {"0"}, "photo_fit": {"stretch"}}); code != http.StatusBadRequest {
		t.Errorf("an unknown fit = %d", code)
	}
}
