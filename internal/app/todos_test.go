package app_test

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/DeRusto/goCasa/internal/app"
	"github.com/DeRusto/goCasa/internal/apptest"
)

func post(t *testing.T, h *apptest.Harness, path string, f url.Values) {
	t.Helper()
	if code, body := h.Post(t, path, f); code != http.StatusOK {
		t.Fatalf("POST %s = %d %q", path, code, body)
	}
}

func todo(column, title, due string) url.Values {
	return url.Values{"column_id": {column}, "title": {title}, "due_date": {due}}
}

var todoTitle = regexp.MustCompile(`data-todo="\d+"[^>]*>\s*(?:<[^>]*>\s*)*([^<]+?)\s*<`)

// column is the to-do titles column id shows on page, in order.
func column(t *testing.T, page, id string) []string {
	t.Helper()
	_, rest, ok := strings.Cut(page, `data-column="`+id+`"`)
	if !ok {
		t.Fatalf("no column %s in page %q", id, page)
	}
	s, _, _ := strings.Cut(rest, "</section>")
	var titles []string
	for _, m := range todoTitle.FindAllStringSubmatch(s, -1) {
		titles = append(titles, m[1])
	}
	return titles
}

func TestToDosAndColumnsCanBeCreatedEditedCompletedAndDeleted(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	post(t, h, "/todo-columns", url.Values{"name": {"House"}})
	post(t, h, "/todo-columns", url.Values{"name": {"Garden"}})
	post(t, h, "/todos", todo("1", "Fix tap", "2026-03-20"))
	post(t, h, "/todos", todo("1", "Paint fence", ""))
	if code, _ := h.Post(t, "/todos", todo("1", "", "")); code != http.StatusBadRequest {
		t.Fatalf("empty title = %d, want 400", code)
	}

	_, page := h.Get(t, "/todos")
	if got := strings.Join(column(t, page, "1"), "|"); got != "Fix tap|Paint fence" {
		t.Fatalf("House = %q", got)
	}
	if !strings.Contains(page, "House") || !strings.Contains(page, "Garden") {
		t.Fatalf("column names missing: %q", page)
	}

	if code, body := h.Get(t, "/todos/2"); code != http.StatusOK || !strings.Contains(body, `value="Paint fence"`) {
		t.Fatalf("edit form = %d %q", code, body)
	}
	post(t, h, "/todos/2", url.Values{"column_id": {"2"}, "title": {"Paint the fence"}, "notes": {"white"}, "due_date": {"2026-03-21"}})
	post(t, h, "/todo-columns/1", url.Values{"name": {"Indoors"}})
	_, page = h.Get(t, "/todos")
	if got := strings.Join(column(t, page, "2"), "|"); got != "Paint the fence" {
		t.Fatalf("moved to-do: Garden = %q", got)
	}
	if !strings.Contains(page, "Indoors") {
		t.Fatal("renamed column missing")
	}

	post(t, h, "/todos/1/complete", nil)
	if _, page = h.Get(t, "/todos"); len(column(t, page, "1")) != 0 {
		t.Fatal("completed to-do still shows among open ones")
	}
	if _, page = h.Get(t, "/todos?status=done"); strings.Join(column(t, page, "1"), "|") != "Fix tap" {
		t.Fatal("completed to-do missing from done")
	}
	post(t, h, "/todos/1/reopen", nil)
	if _, page = h.Get(t, "/todos"); strings.Join(column(t, page, "1"), "|") != "Fix tap" {
		t.Fatal("reopened to-do missing")
	}

	post(t, h, "/todos/1/delete", nil)
	if code, _ := h.Get(t, "/todos/1"); code != http.StatusNotFound {
		t.Fatalf("deleted to-do = %d, want 404", code)
	}
	post(t, h, "/todo-columns/2/delete", nil)
	if _, page = h.Get(t, "/todos"); strings.Contains(page, `data-column="2"`) || strings.Contains(page, "Paint the fence") {
		t.Fatal("deleted column or its to-dos still show")
	}
}

// Dragging sends a column's whole order; a to-do dragged in from another
// column moves there. The order survives a restart.
func TestReorderPersistsAcrossRestartIncludingFirstAndLast(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	post(t, h, "/todo-columns", url.Values{"name": {"House"}})
	post(t, h, "/todo-columns", url.Values{"name": {"Garden"}})
	for _, title := range []string{"A", "B", "C", "D"} {
		post(t, h, "/todos", todo("1", title, ""))
	}
	post(t, h, "/todos", todo("2", "E", ""))

	post(t, h, "/todos/reorder", url.Values{"column_id": {"1"}, "ids": {"4", "1", "2", "3"}}) // D to first
	post(t, h, "/todos/reorder", url.Values{"column_id": {"1"}, "ids": {"4", "2", "3", "1"}}) // A to last
	post(t, h, "/todos/reorder", url.Values{"column_id": {"2"}, "ids": {"5", "3"}})           // C to Garden, last
	post(t, h, "/todos", todo("1", "F", ""))                                                  // new goes last
	if code, _ := h.Post(t, "/todos/reorder", url.Values{"column_id": {"1"}, "ids": {"99"}}); code != http.StatusBadRequest {
		t.Fatalf("unknown to-do = %d, want 400", code)
	}

	h.App.Close()
	a, err := app.New(context.Background(), app.Options{Config: h.App.Config, Logger: h.App.Log, Now: h.Clock.Now})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h.App, h.Server.Config.Handler = a, a.Handler()

	_, page := h.Get(t, "/todos")
	if got := strings.Join(column(t, page, "1"), "|"); got != "D|B|A|F" {
		t.Errorf("House = %q, want D|B|A|F", got)
	}
	if got := strings.Join(column(t, page, "2"), "|"); got != "E|C" {
		t.Errorf("Garden = %q, want E|C", got)
	}
}

func TestToDosSortAndFilterByDueRangeAndStatus(t *testing.T) {
	t.Parallel()
	h := apptest.New(t) // today is 2026-03-15
	post(t, h, "/todo-columns", url.Values{"name": {"House"}})
	post(t, h, "/todos", todo("1", "Later", "2026-04-30"))
	post(t, h, "/todos", todo("1", "Undated", ""))
	post(t, h, "/todos", todo("1", "Overdue", "2026-03-10"))
	post(t, h, "/todos", todo("1", "Today", "2026-03-15"))
	post(t, h, "/todos", todo("1", "Soon", "2026-03-18"))
	post(t, h, "/todos", todo("1", "Done early", "2026-03-12"))
	post(t, h, "/todos/6/complete", nil)

	for q, want := range map[string]string{
		"":                               "Later|Undated|Overdue|Today|Soon",
		"?sort=due":                      "Overdue|Today|Soon|Later|Undated",
		"?status=all&sort=due":           "Overdue|Done early|Today|Soon|Later|Undated",
		"?status=done":                   "Done early",
		"?due=overdue":                   "Overdue",
		"?due=today":                     "Overdue|Today",
		"?due=week&sort=due":             "Overdue|Today|Soon",
		"?due=none":                      "Undated",
		"?due=none&from=2026-03-15":      "Later|Today|Soon",
		"?from=2026-03-15&to=2026-03-18": "Today|Soon",
		"?status=all&to=2026-03-12":      "Overdue|Done early",
		"?due=nonsense&status=nonsense":  "Later|Undated|Overdue|Today|Soon",
	} {
		_, page := h.Get(t, "/todos"+q)
		if got := strings.Join(column(t, page, "1"), "|"); got != want {
			t.Errorf("/todos%s = %q, want %q", q, got, want)
		}
	}
}

func TestCompletedToDosCannotBeEdited(t *testing.T) {
	t.Parallel()
	h := household(t)
	post(t, h, "/todo-columns", url.Values{"name": {"House"}})
	post(t, h, "/todos", todo("1", "Fix tap", ""))
	post(t, h, "/todos/1/complete", nil)
	if code, _ := h.Post(t, "/todos/1", todo("1", "Changed", "")); code != http.StatusConflict {
		t.Fatalf("editing a completed to-do = %d, want 409", code)
	}
	if _, page := h.Get(t, "/todos?status=done"); strings.Join(column(t, page, "1"), "|") != "Fix tap" {
		t.Fatalf("completed to-do was edited: %q", column(t, page, "1"))
	}
}
