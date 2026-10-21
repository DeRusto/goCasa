package app_test

import (
	"html"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// liveTag is the opening tag of page's #live region, unescaped.
func liveTag(t *testing.T, path, page string) string {
	t.Helper()
	tag := regexp.MustCompile(`<div id="live"[^>]*>`).FindString(page)
	if tag == "" {
		t.Fatalf("%s has no #live region", path)
	}
	return tag
}

func attr(tag, name string) string {
	m := regexp.MustCompile(` ` + name + `="([^"]*)"`).FindStringSubmatch(tag)
	if m == nil {
		return ""
	}
	return html.UnescapeString(m[1])
}

func TestPagesRefetchOnlyForTheKindsTheyShow(t *testing.T) {
	h := household(t)
	for path, kinds := range map[string][]string{
		"/calendar":    {"event", "member", "household", "integration"},
		"/lists":       {"list", "integration"},
		"/todos":       {"todo", "member", "household"},
		"/chores":      {"chore", "points", "member", "household"},
		"/today":       {"event", "todo", "chore", "member", "household", "integration"},
		"/screensaver": {"event", "household", "integration"},
	} {
		_, page := h.Get(t, path)
		tag := liveTag(t, path, page)
		if attr(tag, "hx-ext") != "sse" || attr(tag, "sse-connect") != "/events/stream" {
			t.Errorf("%s's #live isn't on the change stream: %s", path, tag)
		}
		var got []string
		for _, tr := range strings.Split(attr(tag, "hx-trigger"), ",") {
			got = append(got, strings.TrimSpace(tr))
		}
		want := []string{"htmx:sseOpen", "refresh-live from:document"} // catch up on (re)connect; a page's own timer
		for _, k := range kinds {
			want = append(want, "sse:"+k)
		}
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Errorf("%s refetches on %v, want %v", path, got, want)
		}
		if !strings.Contains(attr(tag, "hx-headers"), `"X-Background":"1"`) {
			t.Errorf("%s's refetch isn't marked background: %s", path, tag)
		}
		if strings.Contains(page, "new EventSource") {
			t.Errorf("%s still opens its own EventSource", path)
		}
	}
}

// Night mode's reload rides the page's one htmx stream, which must deliver household.
func TestNightPagesWatchHouseholdOnTheirOneStream(t *testing.T) {
	h := household(t)
	nightMode(t, h, "dim_clock")
	for at, night := range map[time.Time]bool{
		time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC): false,
		time.Date(2026, 3, 15, 23, 0, 0, 0, time.UTC): true,
	} {
		h.Clock.Set(at)
		_, page := h.Get(t, "/screensaver")
		if strings.Contains(page, "data-night=") != night {
			t.Fatalf("at %s Night is %v", at, !night)
		}
		conns := regexp.MustCompile(`<[^>]* sse-connect="/events/stream"[^>]*>`).FindAllString(page, -1)
		if len(conns) != 1 || !strings.Contains(attr(conns[0], "hx-trigger"), "sse:household") || strings.Contains(page, "new EventSource") {
			t.Errorf("at %s the page's streams are %q", at, conns)
		}
		if !strings.Contains(page, `addEventListener("sse:household", () => location.reload())`) {
			t.Errorf("at %s the page doesn't reload on a household change", at)
		}
	}
}

// To-dos and local lists reorder by drag with vendored SortableJS, not a hand-rolled script.
func TestDragPagesLoadVendoredSortable(t *testing.T) {
	h := household(t)
	for _, path := range []string{"/todos", "/lists"} {
		_, page := h.Get(t, path)
		if !strings.Contains(page, `<script src="/static/Sortable-1.15.6.min.js"></script>`) {
			t.Errorf("%s doesn't load SortableJS 1.15.6", path)
		}
		if strings.Contains(page, "pointerdown") {
			t.Errorf("%s still has the hand-rolled drag script", path)
		}
	}
	code, body := h.Get(t, "/static/Sortable-1.15.6.min.js")
	if code != http.StatusOK || !strings.HasPrefix(body, "/*! Sortable 1.15.6 ") {
		t.Errorf("GET Sortable = %d %.40q", code, body)
	}
}

func TestPagesLoadVendoredHtmxFromGoCasa(t *testing.T) {
	h := household(t)
	src := regexp.MustCompile(`<script src="([^"]+)"`)
	for _, path := range []string{"/calendar", "/screensaver"} {
		_, page := h.Get(t, path)
		var got string
		for _, m := range src.FindAllStringSubmatch(page, -1) {
			if !strings.HasPrefix(m[1], "/static/") {
				t.Errorf("%s loads %s from outside GoCasa", path, m[1])
			}
			code, body := h.Get(t, m[1])
			if code != http.StatusOK {
				t.Errorf("GET %s = %d", m[1], code)
			}
			got += body
		}
		if !strings.Contains(got, `version:"2.0.11"`) || !strings.Contains(got, `htmx.defineExtension('sse'`) {
			t.Errorf("%s doesn't load htmx 2.0.11 and its SSE extension", path)
		}
	}
}
