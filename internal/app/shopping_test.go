package app_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/DeRusto/goCasa/internal/apptest"
)

// shopper is a locked device of a set-up Household: shopping needs no PIN.
func shopper(t *testing.T) *apptest.Harness {
	t.Helper()
	h := apptest.New(t)
	setup(t, h)
	return h.Device()
}

// items is the item names list id shows, in order.
func items(t *testing.T, h *apptest.Harness, list string) []string {
	t.Helper()
	_, page := h.Get(t, "/lists")
	_, rest, ok := strings.Cut(page, `data-list="`+list+`"`)
	if !ok {
		t.Fatalf("no list %s in %q", list, page)
	}
	rest, _, _ = strings.Cut(rest, "</section>")
	var names []string
	for _, part := range strings.Split(rest, `data-item="`)[1:] {
		_, after, _ := strings.Cut(part, `>`)
		name, _, _ := strings.Cut(after, "<")
		names = append(names, strings.TrimSpace(name))
	}
	return names
}

func TestCreateListsAndItemsWithoutAPIN(t *testing.T) {
	h := shopper(t)
	post(t, h, "/lists", url.Values{"name": {"Groceries"}})
	post(t, h, "/lists", url.Values{"name": {"Hardware"}})
	post(t, h, "/lists/1/items", url.Values{"note": {"Milk"}})
	post(t, h, "/lists/1/items", url.Values{"note": {"Eggs"}})
	post(t, h, "/lists/2/items", url.Values{"note": {"Nails"}})

	if got := strings.Join(items(t, h, "1"), ","); got != "Milk,Eggs" {
		t.Errorf("Groceries = %s", got)
	}
	if got := strings.Join(items(t, h, "2"), ","); got != "Nails" {
		t.Errorf("Hardware = %s", got)
	}
	if code, _ := h.Post(t, "/lists", url.Values{"name": {" "}}); code != http.StatusBadRequest {
		t.Errorf("blank list name = %d, want 400", code)
	}
	if code, _ := h.Post(t, "/lists/1/items", url.Values{"note": {""}}); code != http.StatusBadRequest {
		t.Errorf("blank item = %d, want 400", code)
	}
	if code, _ := h.Post(t, "/lists/9/items", url.Values{"note": {"Bread"}}); code != http.StatusNotFound {
		t.Errorf("item on a missing list = %d, want 404", code)
	}
}

func TestCheckAndUncheckItems(t *testing.T) {
	h := shopper(t)
	post(t, h, "/lists", url.Values{"name": {"Groceries"}})
	post(t, h, "/lists/1/items", url.Values{"note": {"Milk"}})
	post(t, h, "/lists/1/items", url.Values{"note": {"Eggs"}})

	checked := func() string {
		_, page := h.Get(t, "/lists")
		var on []string
		for _, n := range []string{"1", "2"} {
			if strings.Contains(page, `data-item="`+n+`" data-checked`) {
				on = append(on, n)
			}
		}
		return strings.Join(on, ",")
	}
	post(t, h, "/lists/1/items/2", url.Values{"checked": {"true"}})
	if got := checked(); got != "2" {
		t.Fatalf("checked after checking Eggs = %q", got)
	}
	post(t, h, "/lists/1/items/2", url.Values{"checked": {"false"}})
	if got := checked(); got != "" {
		t.Fatalf("checked after unchecking Eggs = %q", got)
	}
	if code, _ := h.Post(t, "/lists/1/items/9", url.Values{"checked": {"true"}}); code != http.StatusNotFound {
		t.Errorf("checking a missing item = %d, want 404", code)
	}
	if code, _ := h.Post(t, "/lists/1/items/1", url.Values{"checked": {"maybe"}}); code != http.StatusBadRequest {
		t.Errorf("checked=maybe = %d, want 400", code)
	}
}

func TestReorderPersists(t *testing.T) {
	h := shopper(t)
	post(t, h, "/lists", url.Values{"name": {"Groceries"}})
	for _, n := range []string{"Milk", "Eggs", "Bread", "Jam"} {
		post(t, h, "/lists/1/items", url.Values{"note": {n}})
	}
	move := func(item, to string) (int, string) {
		return h.Post(t, "/lists/1/items/"+item+"/move", url.Values{"position": {to}})
	}
	for _, c := range []struct{ item, to, want string }{
		{"4", "0", "Jam,Milk,Eggs,Bread"}, // last to first
		{"4", "3", "Milk,Eggs,Bread,Jam"}, // first to last
		{"1", "2", "Eggs,Bread,Milk,Jam"},
		{"1", "2", "Eggs,Bread,Milk,Jam"}, // where it already is
	} {
		if code, body := move(c.item, c.to); code != http.StatusOK {
			t.Fatalf("move %s to %s = %d %q", c.item, c.to, code, body)
		}
		// A fresh device: the order is stored, not remembered by this browser.
		if got := strings.Join(items(t, h.Device(), "1"), ","); got != c.want {
			t.Fatalf("after moving %s to %s: %s, want %s", c.item, c.to, got, c.want)
		}
	}
	for _, to := range []string{"-1", "4", "x"} {
		if code, _ := move("1", to); code != http.StatusBadRequest {
			t.Errorf("move to %s = %d, want 400", to, code)
		}
	}
	if code, _ := move("9", "0"); code != http.StatusNotFound {
		t.Errorf("moving a missing item = %d, want 404", code)
	}
}

func TestListsPageFollowsLiveUpdates(t *testing.T) {
	h := household(t)
	if _, page := h.Get(t, "/lists"); !strings.Contains(page, `id="live"`) || !strings.Contains(page, "/events/stream") {
		t.Fatalf("Lists has no live region: %q", page)
	}
}
