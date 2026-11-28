package app_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/DeRusto/goCasa/internal/apptest"
)

// memberTodos is the to-do titles Member id's page shows, in order.
func memberTodos(t *testing.T, h *apptest.Harness, id string) []string {
	t.Helper()
	s := section(t, memberPage(t, h, id), "member-todos")
	var titles []string
	for _, m := range todoTitle.FindAllStringSubmatch(s, -1) {
		titles = append(titles, m[1])
	}
	return titles
}

// columnFlyout is column id's section on the To-dos page, its flyout included.
func columnFlyout(t *testing.T, page, id string) string {
	t.Helper()
	_, s, ok := strings.Cut(page, `data-column="`+id+`"`)
	if !ok {
		t.Fatalf("no column %s in %q", id, page)
	}
	s, _, _ = strings.Cut(s, "</section>")
	return s
}

// columnOf gives column id to Member member ("" for none), keeping its name.
func columnOf(t *testing.T, h *apptest.Harness, id, name, member string) {
	t.Helper()
	post(t, h, "/todo-columns/"+id, url.Values{"name": {name}, "member_id": {member}})
}

// A column belongs to no Member until someone picks one from the To-dos page.
func TestToDoColumnsBelongToNoMemberByDefault(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/todo-columns", url.Values{"name": {"Bob's jobs"}})
	post(t, h, "/todos", todo("1", "Tidy room", ""))
	if got := memberTodos(t, h, "2"); len(got) != 0 {
		t.Errorf("a new column's to-dos show on Bob's page: %q", got)
	}
	if n := count(t, h, "SELECT count(*) FROM todo_columns WHERE member_id IS NOT NULL"); n != 0 {
		t.Errorf("%d new columns have a Member", n)
	}
}

// The page shows the open to-dos of the Member's columns that are overdue,
// due today or undated, in that order; never later, completed or others'.
func TestMemberPageShowsItsColumnsOpenToDosDueByToday(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t) // Sunday 15 March 2026
	post(t, h, "/todo-columns", url.Values{"name": {"Bob's jobs"}})
	post(t, h, "/todo-columns", url.Values{"name": {"House"}})
	post(t, h, "/todo-columns", url.Values{"name": {"Bob's school"}})
	columnOf(t, h, "1", "Bob's jobs", "2")
	columnOf(t, h, "3", "Bob's school", "2")
	post(t, h, "/todos", todo("1", "Tidy room", ""))
	post(t, h, "/todos", todo("1", "Pack bag", "2026-03-15"))
	post(t, h, "/todos", todo("1", "Return book", "2026-03-10"))
	post(t, h, "/todos", todo("1", "Haircut", "2026-03-16"))
	post(t, h, "/todos", todo("1", "Old job", "2026-03-15"))
	post(t, h, "/todos/5/complete", nil)
	post(t, h, "/todos", todo("2", "Fix tap", "2026-03-15"))
	post(t, h, "/todos", todo("3", "Homework", "2026-03-14"))

	if got := strings.Join(memberTodos(t, h, "2"), "|"); got != "Return book|Homework|Pack bag|Tidy room" {
		t.Errorf("Bob's to-dos = %q, want overdue, then today, then undated", got)
	}
	if got := memberTodos(t, h, "3"); len(got) != 0 {
		t.Errorf("Carol's page shows to-dos of columns that aren't hers: %q", got)
	}
}

// A few at most: the rest wait on the To-dos page.
func TestMemberPageCapsItsToDos(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/todo-columns", url.Values{"name": {"Bob's jobs"}})
	columnOf(t, h, "1", "Bob's jobs", "2")
	for _, title := range []string{"One", "Two", "Three", "Four", "Five", "Six", "Seven"} {
		post(t, h, "/todos", todo("1", title, ""))
	}
	if got := strings.Join(memberTodos(t, h, "2"), "|"); got != "One|Two|Three|Four|Five" {
		t.Errorf("Bob's capped to-dos = %q, want the first five", got)
	}
	if s := section(t, memberPage(t, h, "2"), "member-todos"); !strings.Contains(s, "2 more") || !strings.Contains(s, `href="/todos"`) {
		t.Errorf("the cap doesn't say how many more wait on the To-dos page: %q", s)
	}
}

// Anyone sets or clears a column's Member from its flyout on the To-dos page,
// picking among the Members with a Member page.
func TestAnyoneSetsAndClearsAColumnsMemberFromTheToDosPage(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t) // Ada 1, Bob 2, Carol 3, Rex 4 (a pet)
	post(t, h, "/settings/members", member("Dan", "#0ea5e9", "member", ""))
	post(t, h, "/settings/members/5/archive", nil)
	post(t, h, "/todo-columns", url.Values{"name": {"Jobs"}})
	post(t, h, "/todos", todo("1", "Tidy room", ""))
	post(t, h, "/lock/now", nil) // a locked device still can: it's like renaming

	_, page := h.Get(t, "/todos")
	picker := columnFlyout(t, page, "1")
	for _, want := range []string{`name="member_id"`, `<option value="" selected`, ">Bob<", ">Carol<", ">Ada<"} {
		if !strings.Contains(picker, want) {
			t.Errorf("the column flyout lacks %s: %q", want, picker)
		}
	}
	if strings.Contains(picker, ">Rex<") || strings.Contains(picker, "Dan") {
		t.Errorf("the picker offers a pet or an Archived Member: %q", picker)
	}

	columnOf(t, h, "1", "Jobs", "2")
	if got := memberTodos(t, h, "2"); len(got) != 1 {
		t.Fatalf("after picking Bob, his page shows %q", got)
	}
	_, page = h.Get(t, "/todos")
	if !strings.Contains(page, `<option value="2" selected`) {
		t.Errorf("the flyout doesn't show Bob picked: %q", page)
	}
	post(t, h, "/todo-columns/1", url.Values{"name": {"Chores"}}) // a rename without the field keeps the Member
	if got := memberTodos(t, h, "2"); len(got) != 1 {
		t.Errorf("a plain rename dropped the column's Member: %q", got)
	}
	columnOf(t, h, "1", "Chores", "")
	if got := memberTodos(t, h, "2"); len(got) != 0 {
		t.Errorf("after clearing, Bob's page still shows %q", got)
	}

	for _, bad := range []string{"4", "5", "99", "x"} {
		if code, _ := h.Post(t, "/todo-columns/1", url.Values{"name": {"Chores"}, "member_id": {bad}}); code != http.StatusBadRequest {
			t.Errorf("member_id %s = %d, want 400", bad, code)
		}
	}
}

// Ticking a to-do on the Member page completes it and comes back there.
func TestTickingAToDoFromTheMemberPageComesBack(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/todo-columns", url.Values{"name": {"Bob's jobs"}})
	columnOf(t, h, "1", "Bob's jobs", "2")
	post(t, h, "/todos", todo("1", "Tidy room", ""))
	post(t, h, "/todos", todo("1", "Pack bag", ""))
	if s := section(t, memberPage(t, h, "2"), "member-todos"); !strings.Contains(s, `action="/todos/1/complete"`) || !strings.Contains(s, `name="next" value="/members/2"`) {
		t.Fatalf("no tick that returns to Bob's page: %q", s)
	}

	code, page := h.Post(t, "/todos/1/complete", url.Values{"next": {"/members/2"}})
	if code != http.StatusOK || !strings.Contains(page, `data-member-page="2"`) {
		t.Fatalf("ticking returned %d to %q, want Bob's page", code, page)
	}
	if got := strings.Join(memberTodos(t, h, "2"), "|"); got != "Pack bag" {
		t.Errorf("after ticking, Bob's to-dos = %q", got)
	}

	h.Client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for next, want := range map[string]string{"": "/todos", "//evil.example": "/todos", "/members/2/avatar": "/todos", "/members/2": "/members/2"} {
		h.App.DB.Exec("UPDATE todos SET completed_at = NULL")
		res, err := h.Client.PostForm(h.Server.URL+"/todos/2/complete", url.Values{"next": {next}})
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if got := res.Header.Get("Location"); got != want {
			t.Errorf("next %q went to %q, want %q", next, got, want)
		}
	}
}

func TestTickingAToDoFromTheMemberPageFollowsTheToDosLock(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/todo-columns", url.Values{"name": {"Bob's jobs"}})
	columnOf(t, h, "1", "Bob's jobs", "2")
	post(t, h, "/todos", todo("1", "Tidy room", ""))
	post(t, h, "/settings/locks", url.Values{"lock_todos": {"on"}})
	kid := h.Device()
	body := postFrom(t, kid, "/members/2", "/todos/1/complete", url.Values{"next": {"/members/2"}})
	if !strings.Contains(body, lockPage) || !strings.Contains(body, `name="next" value="/members/2"`) {
		t.Fatalf("a locked tick didn't ask for the PIN and come back: %q", body)
	}
	if n := count(t, h, "SELECT count(*) FROM todos WHERE completed_at IS NOT NULL"); n != 0 {
		t.Fatalf("a locked tick completed %d to-dos", n)
	}
}

// An Archived Member keeps their column (it shows again once restored);
// deleting a Member clears it.
func TestArchivingKeepsAColumnsMemberAndDeletingClearsIt(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/settings/members", member("Dan", "#0ea5e9", "member", ""))
	post(t, h, "/todo-columns", url.Values{"name": {"Carol's jobs"}})
	post(t, h, "/todo-columns", url.Values{"name": {"Dan's jobs"}})
	columnOf(t, h, "1", "Carol's jobs", "3")
	columnOf(t, h, "2", "Dan's jobs", "5")
	post(t, h, "/todos", todo("1", "Feed cat", ""))

	post(t, h, "/settings/members/3/archive", nil)
	_, page := h.Get(t, "/todos")
	if picker := columnFlyout(t, page, "1"); !strings.Contains(picker, `<option value="3" selected`) {
		t.Errorf("an Archived Member's column lost its Member in the flyout: %q", picker)
	}
	columnOf(t, h, "1", "Carol's chores", "3") // renaming keeps them
	post(t, h, "/settings/members/3/restore", nil)
	if got := memberTodos(t, h, "3"); len(got) != 1 {
		t.Errorf("Carol's column didn't keep her through archiving: %q", got)
	}

	post(t, h, "/settings/members/5/delete", nil)
	if n := count(t, h, "SELECT count(*) FROM todo_columns WHERE id = 2 AND member_id IS NULL"); n != 1 {
		t.Error("deleting Dan didn't clear his column's Member")
	}
}

// The page refreshes when a to-do or column changes.
func TestMemberPageRefreshesOnToDoChanges(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	if page := memberPage(t, h, "2"); !strings.Contains(page, "sse:todo") {
		t.Errorf("the Member page doesn't refresh on to-do changes: %q", page)
	}
}
