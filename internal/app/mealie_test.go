package app_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/apptest"
)

// fakeMealie is a Mealie household's shopping-list API, kept in memory. Like
// the real one, an item PUT replaces the whole item, and an add with the same
// note as an unchecked item merges into it.
type fakeMealie struct {
	mu      sync.Mutex
	version string
	lists   [][2]string               // id, name
	items   map[string]map[string]any // by id
	order   []string                  // item ids in creation order
	down    bool                      // answer 503 to everything
	garbled bool                      // answer 200 with a body that isn't JSON
	puts    []map[string]any          // each item PUT body
	auth    []string                  // each request's Authorization
	gone    map[string]bool           // items deleted in Mealie behind GoCasa's back, for GET/PUT 404
	next    int
	*httptest.Server
}

const mealieToken = "tok-123"

func newMealie(t *testing.T) *fakeMealie {
	m := &fakeMealie{version: "v3.29.0", items: map[string]map[string]any{}, gone: map[string]bool{}}
	m.Server = httptest.NewServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.Close)
	return m
}

func (m *fakeMealie) addList(id, name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.lists = append(m.lists, [2]string{id, name})
}

// addItem adds an item as Mealie's own UI would, with fields GoCasa must keep.
func (m *fakeMealie) addItem(list, id, display string, checked bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[id] = map[string]any{"id": id, "shoppingListId": list, "note": display, "display": display, "checked": checked,
		"quantity": 2.0, "position": len(m.order), "foodId": "food-" + id,
		"extras": map[string]any{"aisle": "7"}, "recipeReferences": []any{map[string]any{"id": "ref-" + id, "recipeId": "r1", "recipeQuantity": 1.0}}}
	m.order = append(m.order, id)
}

func (m *fakeMealie) item(id string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.items[id]
}

func (m *fakeMealie) remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, id)
	m.order = slices.DeleteFunc(m.order, func(s string) bool { return s == id })
}

func (m *fakeMealie) setDown(d bool)    { m.mu.Lock(); m.down = d; m.mu.Unlock() }
func (m *fakeMealie) setGarbled(g bool) { m.mu.Lock(); m.garbled = g; m.mu.Unlock() }
func (m *fakeMealie) requests() int     { m.mu.Lock(); defer m.mu.Unlock(); return len(m.auth) }

func (m *fakeMealie) listItems(list string) []any {
	out := []any{}
	for _, id := range m.order {
		if it := m.items[id]; it["shoppingListId"] == list {
			out = append(out, it)
		}
	}
	return out
}

func (m *fakeMealie) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.auth = append(m.auth, r.Header.Get("Authorization"))
	if m.down {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if m.garbled {
		w.Write([]byte("<html>maintenance</html>"))
		return
	}
	if r.URL.Path == "/api/app/about" {
		json.NewEncoder(w).Encode(map[string]any{"version": m.version})
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+mealieToken {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	reply := func(v any) { json.NewEncoder(w).Encode(v) }
	collection := func(created, updated, deleted []any) {
		reply(map[string]any{"createdItems": created, "updatedItems": updated, "deletedItems": deleted})
	}
	const lists, items = "/api/households/shopping/lists", "/api/households/shopping/items"
	switch path := r.URL.Path; {
	case r.Method == "GET" && path == lists:
		var out []any
		for _, l := range m.lists {
			out = append(out, map[string]any{"id": l[0], "name": l[1]})
		}
		reply(map[string]any{"page": 1, "per_page": -1, "items": out})
	case r.Method == "GET" && strings.HasPrefix(path, lists+"/"):
		id := strings.TrimPrefix(path, lists+"/")
		for _, l := range m.lists {
			if l[0] == id {
				reply(map[string]any{"id": id, "name": l[1], "listItems": m.listItems(id)})
				return
			}
		}
		http.NotFound(w, r)
	case r.Method == "POST" && path == items:
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in)
		for _, id := range m.order {
			if it := m.items[id]; it["shoppingListId"] == in["shoppingListId"] && it["note"] == in["note"] && it["checked"] == false {
				it["quantity"] = it["quantity"].(float64) + 1
				it["display"] = fmt.Sprintf("%v %v", it["quantity"], it["note"])
				collection(nil, []any{it}, nil)
				return
			}
		}
		m.next++
		id := fmt.Sprintf("new-%d", m.next)
		in["id"], in["checked"], in["display"], in["quantity"] = id, false, in["note"], 1.0
		m.items[id] = in
		m.order = append(m.order, id)
		collection([]any{in}, nil, nil)
	case r.Method == "DELETE" && path == items:
		for _, id := range r.URL.Query()["ids"] {
			delete(m.items, id)
			m.order = slices.DeleteFunc(m.order, func(s string) bool { return s == id })
		}
		reply(map[string]any{"message": "ok"})
	case strings.HasPrefix(path, items+"/"):
		id := strings.TrimPrefix(path, items+"/")
		it, ok := m.items[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case "GET":
			reply(it)
		case "PUT":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			m.puts = append(m.puts, in)
			m.items[id] = in // a full replace: whatever isn't sent is lost
			collection(nil, []any{in}, nil)
		case "DELETE":
			delete(m.items, id)
			m.order = slices.DeleteFunc(m.order, func(s string) bool { return s == id })
			reply(map[string]any{"message": "ok"})
		}
	default:
		http.NotFound(w, r)
	}
}

// mealieHousehold is a set-up, unlocked Household with Mealie connected and
// synced, and a locked device d for shopping.
func mealieHousehold(t *testing.T) (h, d *apptest.Harness, m *fakeMealie) {
	t.Helper()
	m = newMealie(t)
	m.addList("L1", "Groceries")
	m.addList("L2", "Costco")
	m.addItem("L1", "I1", "2 Milk", false)
	m.addItem("L1", "I2", "Eggs", true)
	m.addItem("L2", "I3", "Paper towels", false)
	h = apptest.New(t)
	setup(t, h)
	connectMealie(t, h, m.URL)
	return h, h.Device(), m
}

func connectMealie(t *testing.T, h *apptest.Harness, u string) {
	t.Helper()
	if code, page := h.Post(t, "/settings/mealie", url.Values{"url": {u + "/"}, "token": {mealieToken}}); code != http.StatusOK || !strings.Contains(page, "Mealie") {
		t.Fatalf("connect Mealie = %d %q", code, page)
	}
}

// listByName is the item names on the list named name, as /lists shows it.
func listByName(t *testing.T, h *apptest.Harness, name string) []string {
	t.Helper()
	_, page := h.Get(t, "/lists")
	_, rest, ok := strings.Cut(page, "<h2>"+name+"</h2>")
	if !ok {
		return nil
	}
	rest, _, _ = strings.Cut(rest, "</section>")
	var names []string
	for _, part := range strings.Split(rest, `data-item="`)[1:] {
		_, after, _ := strings.Cut(part, `>`)
		n, _, _ := strings.Cut(after, "<")
		if strings.Contains(part[:strings.Index(part, ">")], "data-checked") {
			n = "[x] " + strings.TrimSpace(n)
		}
		names = append(names, strings.TrimSpace(n))
	}
	return names
}

func TestMealieConnectMirrorsEveryListWithMealiesDisplay(t *testing.T) {
	t.Parallel()
	_, d, m := mealieHousehold(t)
	if got := strings.Join(listByName(t, d, "Groceries"), ","); got != "2 Milk,[x] Eggs" {
		t.Errorf("Groceries = %q", got)
	}
	if got := strings.Join(listByName(t, d, "Costco"), ","); got != "Paper towels" {
		t.Errorf("Costco = %q", got)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.auth[1:] { // /api/app/about is public
		if a != "Bearer "+mealieToken {
			t.Fatalf("Authorization = %q", a)
		}
	}
}

func TestMealieRefusesOldVersionsAndBadAddresses(t *testing.T) {
	t.Parallel()
	m := newMealie(t)
	m.version = "v1.12.0"
	m.addList("l1", "Groceries")
	h := apptest.New(t)
	setup(t, h)
	if code, page := h.Post(t, "/settings/mealie", url.Values{"url": {m.URL}, "token": {mealieToken}}); code != http.StatusBadRequest || !strings.Contains(page, "v2.0") {
		t.Errorf("Mealie v1 = %d %q", code, page)
	}
	m.version = "v3.29.0"
	userinfo, _ := url.Parse(m.URL)
	userinfo.User = url.UserPassword("ada", "secret") // config_json isn't sealed
	if code, _ := h.Post(t, "/settings/mealie", url.Values{"url": {userinfo.String()}, "token": {mealieToken}}); code != http.StatusBadRequest {
		t.Errorf("a password in the address = %d, want 400", code)
	}
	if _, page := h.Get(t, "/lists"); strings.Contains(page, "Groceries") {
		t.Errorf("a refused connect pulled lists: %q", page)
	}

	// A connection is kept, and its lists with it, through refused attempts.
	post(t, h, "/settings/mealie", url.Values{"url": {m.URL}, "token": {mealieToken}})
	for name, f := range map[string]url.Values{
		"ftp address":     {"url": {"ftp://x"}, "token": {mealieToken}},
		"no token":        {"url": {m.URL}, "token": {""}},
		"a second Mealie": {"url": {m.URL}, "token": {mealieToken}},
	} {
		if code, _ := h.Post(t, "/settings/mealie", f); code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", name, code)
		}
	}
	if _, page := h.Get(t, "/lists"); strings.Count(page, "<h2>Groceries</h2>") != 1 {
		t.Errorf("refused connects changed the Mirrored lists: %q", page)
	}
}

// ids are the GoCasa ids of the list named list and its item shown as item.
func ids(t *testing.T, h *apptest.Harness, list, item string) (string, string) {
	t.Helper()
	_, page := h.Get(t, "/lists")
	l := regexp.MustCompile(`data-list="(\d+)">\s*<h2>` + regexp.QuoteMeta(list) + `</h2>`).FindStringSubmatch(page)
	i := regexp.MustCompile(`data-item="(\d+)"[^>]*>` + regexp.QuoteMeta(item) + `<`).FindStringSubmatch(page)
	if l == nil || i == nil {
		t.Fatalf("no %s on %s in %q", item, list, page)
	}
	return l[1], i[1]
}

func TestMealieCheckPutsBackTheWholeFreshItem(t *testing.T) {
	t.Parallel()
	_, d, m := mealieHousehold(t)
	l, i := ids(t, d, "Groceries", "2 Milk")
	m.mu.Lock()
	m.items["I1"]["quantity"] = 3.0 // changed in Mealie since the last pull
	m.mu.Unlock()
	post(t, d, "/lists/"+l+"/items/"+i, url.Values{"checked": {"true"}})

	got := m.item("I1")
	if got["checked"] != true || got["quantity"] != 3.0 || got["foodId"] != "food-I1" || got["note"] != "2 Milk" {
		t.Errorf("Mealie's item = %v", got)
	}
	if ex, _ := got["extras"].(map[string]any); ex["aisle"] != "7" {
		t.Errorf("extras lost: %v", got["extras"])
	}
	if refs, _ := got["recipeReferences"].([]any); len(refs) != 1 || refs[0].(map[string]any)["id"] != "ref-I1" {
		t.Errorf("recipe references lost: %v", got["recipeReferences"])
	}
	if got := strings.Join(listByName(t, d, "Groceries"), ","); got != "[x] 2 Milk,[x] Eggs" {
		t.Errorf("Groceries = %q", got)
	}
	post(t, d, "/lists/"+l+"/items/"+i, url.Values{"checked": {"false"}})
	if m.item("I1")["checked"] != false {
		t.Errorf("uncheck didn't reach Mealie")
	}
}

func TestMealieCheckOfAnItemGoneInMealieRepulls(t *testing.T) {
	t.Parallel()
	_, d, m := mealieHousehold(t)
	l, i := ids(t, d, "Groceries", "2 Milk")
	m.remove("I1")
	code, page := d.Post(t, "/lists/"+l+"/items/"+i, url.Values{"checked": {"true"}})
	if code != http.StatusNotFound || !strings.Contains(page, "That item was removed in Mealie") {
		t.Errorf("check gone item = %d %q", code, page)
	}
	if got := strings.Join(listByName(t, d, "Groceries"), ","); got != "[x] Eggs" {
		t.Errorf("Groceries = %q", got)
	}
}

func TestMealieAddAcceptsMealiesMerge(t *testing.T) {
	t.Parallel()
	_, d, m := mealieHousehold(t)
	l, _ := ids(t, d, "Groceries", "2 Milk")
	post(t, d, "/lists/"+l+"/items", url.Values{"note": {"Bread"}})
	if got := strings.Join(listByName(t, d, "Groceries"), ","); got != "2 Milk,[x] Eggs,Bread" {
		t.Errorf("after add = %q", got)
	}
	post(t, d, "/lists/"+l+"/items", url.Values{"note": {"Bread"}}) // Mealie merges into the first
	if got := strings.Join(listByName(t, d, "Groceries"), ","); got != "2 Milk,[x] Eggs,2 Bread" {
		t.Errorf("after merged add = %q", got)
	}
	m.mu.Lock()
	n := len(m.order)
	m.mu.Unlock()
	if n != 4 {
		t.Errorf("Mealie has %d items, want 4", n)
	}
}

func TestMealieDeleteAndClearChecked(t *testing.T) {
	t.Parallel()
	h, d, m := mealieHousehold(t)
	l, milk := ids(t, d, "Groceries", "2 Milk")
	_, eggs := ids(t, d, "Groceries", "Eggs")
	post(t, d, "/lists/"+l+"/items/"+milk+"/delete", nil)
	if m.item("I1") != nil {
		t.Errorf("Milk still in Mealie")
	}
	m.remove("I2") // already gone in Mealie: still a success
	post(t, d, "/lists/"+l+"/items/"+eggs+"/delete", nil)
	if got := listByName(t, d, "Groceries"); len(got) != 0 {
		t.Errorf("Groceries = %q", got)
	}

	// Clear checked deletes what Mealie has checked now, not what GoCasa last saw.
	m.addItem("L1", "I4", "Jam", true)
	m.addItem("L1", "I5", "Tea", false)
	h.RunDue() // opening /lists made a poll due
	m.mu.Lock()
	m.items["I4"]["checked"], m.items["I5"]["checked"] = false, true
	m.mu.Unlock()
	post(t, d, "/lists/"+l+"/clear", nil)
	if m.item("I5") != nil || m.item("I4") == nil {
		t.Errorf("clear deleted the wrong items: Jam %v, Tea %v", m.item("I4"), m.item("I5"))
	}
	if got := strings.Join(listByName(t, d, "Groceries"), ","); got != "Jam" {
		t.Errorf("Groceries after clear = %q", got)
	}
}

func TestMealiePollsEveryMinuteAndOnListOpen(t *testing.T) {
	t.Parallel()
	h, d, m := mealieHousehold(t)
	m.addItem("L2", "I9", "Batteries", false)
	m.addList("L3", "Party")
	h.Clock.Advance(59 * time.Second)
	h.RunDue()
	if got := listByName(t, d, "Costco"); len(got) != 1 {
		t.Fatalf("polled before 60 s: %q", got)
	}
	h.RunDue() // opening /lists made a poll due
	if got := strings.Join(listByName(t, d, "Costco"), ","); got != "Paper towels,Batteries" {
		t.Errorf("after list open = %q", got)
	}
	if _, page := d.Get(t, "/lists"); !strings.Contains(page, "<h2>Party</h2>") {
		t.Errorf("new Mealie list not mirrored")
	}
	m.remove("I9")
	h.Clock.Advance(60 * time.Second)
	h.RunDue()
	if got := strings.Join(listByName(t, d, "Costco"), ","); got != "Paper towels" {
		t.Errorf("after poll = %q", got)
	}
}

func TestMealieLiveRefreshOfListsDoesNotPoll(t *testing.T) {
	t.Parallel()
	h, d, m := mealieHousehold(t)
	h.RunDue() // the poll a list open made due
	m.addItem("L2", "I9", "Batteries", false)
	h.Clock.Advance(30 * time.Second)
	req, _ := http.NewRequest(http.MethodGet, d.Server.URL+"/lists", nil)
	req.Header.Set("X-Background", "1") // the live-update refetch after a change
	res, err := d.Client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	h.RunDue()
	if got := listByName(t, d, "Costco"); len(got) != 1 {
		t.Errorf("a live refresh made a poll due: %q", got)
	}
}

func TestMealieListsCanBeHidden(t *testing.T) {
	t.Parallel()
	h, d, _ := mealieHousehold(t)
	costco, _ := ids(t, d, "Costco", "Paper towels")
	post(t, h, "/settings/mealie-lists/"+costco, url.Values{"hidden": {"on"}})
	if _, page := d.Get(t, "/lists"); strings.Contains(page, "Costco") {
		t.Errorf("hidden list shows")
	}
	h.Clock.Advance(time.Minute)
	h.RunDue()
	if _, page := d.Get(t, "/lists"); strings.Contains(page, "Costco") {
		t.Errorf("a poll unhid the list")
	}
	post(t, h, "/settings/mealie/delete", nil)
	if _, page := d.Get(t, "/lists"); strings.Contains(page, "Groceries") {
		t.Errorf("Mirrored lists outlive the disconnect")
	}
}

const mealieDown = "reach Mealie"

func TestMealieFailedWriteMakesListsReadOnlyUntilAGoodPoll(t *testing.T) {
	t.Parallel()
	h, d, m := mealieHousehold(t)
	l, i := ids(t, d, "Groceries", "2 Milk")
	m.setDown(true)
	if code, page := d.Post(t, "/lists/"+l+"/items/"+i, url.Values{"checked": {"true"}}); code != http.StatusServiceUnavailable || !strings.Contains(page, mealieDown) {
		t.Fatalf("check while down = %d %q", code, page)
	}
	_, page := d.Get(t, "/lists")
	if !strings.Contains(page, mealieDown) || strings.Contains(page, `aria-label="Check 2 Milk"`) || !strings.Contains(page, "2 Milk") {
		t.Errorf("down lists aren't read-only with their last contents: %q", page)
	}
	if code, _ := d.Post(t, "/lists/"+l+"/items", url.Values{"note": {"Bread"}}); code != http.StatusServiceUnavailable {
		t.Errorf("add while read-only = %d", code)
	}
	m.setDown(false)
	h.Clock.Advance(time.Minute)
	h.RunDue()
	if _, page := d.Get(t, "/lists"); strings.Contains(page, mealieDown) || !strings.Contains(page, `aria-label="Check 2 Milk"`) {
		t.Errorf("still read-only after a good poll: %q", page)
	}
}

func TestMealieThreeFailedPollsMakeListsReadOnly(t *testing.T) {
	t.Parallel()
	h, d, m := mealieHousehold(t)
	m.setDown(true)
	for n := 1; n <= 3; n++ {
		h.Clock.Advance(time.Minute)
		h.RunDue()
		_, page := d.Get(t, "/lists")
		if down := strings.Contains(page, mealieDown); down != (n == 3) {
			t.Errorf("after %d failed polls, read-only = %v", n, down)
		}
	}
	if got := listByName(t, d, "Groceries"); len(got) == 0 || got[0] != "2 Milk" {
		t.Errorf("last contents lost: %q", got)
	}
}

func TestMealieGarbledAnswerBacksOff(t *testing.T) {
	h, _, m := mealieHousehold(t)
	m.setGarbled(true)
	h.Clock.Advance(time.Minute)
	h.RunDue() // fails: next in 1 minute
	h.Clock.Advance(time.Minute)
	h.RunDue() // fails again: next in 2
	n := m.requests()
	h.Clock.Advance(time.Minute)
	h.RunDue()
	if got := m.requests(); got != n {
		t.Fatalf("a garbled answer was retried at the 60s poll pace (%d requests, want %d)", got, n)
	}
	h.Clock.Advance(time.Minute)
	h.RunDue()
	if got := m.requests(); got == n {
		t.Fatal("not retried after its 2-minute backoff")
	}
}

func TestLocalItemsCanBeDeletedAndCleared(t *testing.T) {
	t.Parallel()
	h := shopper(t)
	post(t, h, "/lists", url.Values{"name": {"Groceries"}})
	for _, n := range []string{"Milk", "Eggs", "Bread"} {
		post(t, h, "/lists/1/items", url.Values{"note": {n}})
	}
	post(t, h, "/lists/1/items/1/delete", nil)
	post(t, h, "/lists/1/items/2", url.Values{"checked": {"true"}})
	post(t, h, "/lists/1/clear", nil)
	if got := strings.Join(items(t, h, "1"), ","); got != "Bread" {
		t.Errorf("Groceries = %s", got)
	}
	if code, _ := h.Post(t, "/lists/1/items/1/delete", nil); code != http.StatusNotFound {
		t.Errorf("deleting a missing item = %d, want 404", code)
	}
}
