package app_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/apptest"
)

// chore is the set-up form for a Chore assigned to Members ids.
func chore(name, schedule, kind, points string, ids ...string) url.Values {
	return url.Values{"name": {name}, "schedule": {schedule}, "kind": {kind}, "points": {points}, "assignees": ids}
}

func complete(h *apptest.Harness, t *testing.T, chore, member, round string) int {
	t.Helper()
	code, _ := h.Post(t, "/chores/"+chore+"/complete", url.Values{"member_id": {member}, "round": {round}})
	return code
}

// choreBoard is the chore board's live part.
func choreBoard(t *testing.T, h *apptest.Harness) string {
	t.Helper()
	code, page := h.Get(t, "/chores")
	if code != http.StatusOK {
		t.Fatalf("GET /chores = %d", code)
	}
	_, live, _ := strings.Cut(page, `id="live"`)
	live, _, _ = strings.Cut(live, "<details>")
	return live
}

// choreHousehold is household (Ada, Bob, Carol) plus Rex, a pet (4).
func choreHousehold(t *testing.T) *apptest.Harness {
	t.Helper()
	h := household(t)
	f := member("Rex", "#a16207", "member", "")
	f.Set("pet", "on")
	post(t, h, "/settings/members", f)
	return h
}

func TestAnAdminSetsUpChoresForPeopleOnly(t *testing.T) {
	h := choreHousehold(t) // Sunday 15 March 2026, 09:00

	other := h.Device()
	if !asksForPIN(t, other, "/chores", chore("Dishes", "daily", "each", "5", "2")) {
		t.Fatal("a locked device set up a Chore")
	}
	if code, _ := h.Post(t, "/chores", chore("Feed Rex", "daily", "anyone", "2", "4")); code != http.StatusBadRequest {
		t.Fatalf("assigning a pet = %d, want 400", code)
	}
	if code, _ := h.Post(t, "/chores", chore("Nobody", "daily", "each", "2")); code != http.StatusBadRequest {
		t.Fatalf("a Chore with no assignee = %d, want 400", code)
	}
	if code, _ := h.Post(t, "/chores", chore("Bins", "weekdays", "each", "2", "2")); code != http.StatusBadRequest {
		t.Fatalf("chosen weekdays with no days = %d, want 400", code)
	}
	post(t, h, "/chores", chore("Dishes", "daily", "each", "5", "2", "3"))
	if n := count(t, h, "SELECT count(*) FROM chores"); n != 1 {
		t.Fatalf("chores = %d, want 1", n)
	}

	b := choreBoard(t, other)
	if !strings.Contains(b, "Dishes") || !strings.Contains(b, "Bob") || !strings.Contains(b, "Carol") || strings.Contains(b, "Rex") {
		t.Fatalf("board = %q", b)
	}
}

func TestAPetCannotBeAnAdmin(t *testing.T) {
	h := household(t)
	f := member("Rex", "#a16207", "admin", "1234")
	f.Set("pet", "on")
	if code, _ := h.Post(t, "/settings/members", f); code != http.StatusBadRequest {
		t.Fatalf("pet Admin = %d, want 400", code)
	}
}

func TestEachChoreIsDoneOncePerRoundByEveryAssignee(t *testing.T) {
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Dishes", "daily", "each", "5", "2", "3"))
	kid := h.Device()

	for _, who := range []string{"2", "2", "3"} {
		if code := complete(kid, t, "1", who, "2026-03-15"); code != http.StatusOK {
			t.Fatalf("Member %s completing = %d", who, code)
		}
	}
	if code := complete(kid, t, "1", "1", "2026-03-15"); code != http.StatusBadRequest {
		t.Fatalf("an unassigned Member completing = %d, want 400", code)
	}
	if n := count(t, h, "SELECT count(*) FROM chore_completions"); n != 2 {
		t.Fatalf("completions = %d, want one each for Bob and Carol", n)
	}
	// Points are stored with the completion, so a later change only affects future Rounds.
	post(t, h, "/chores/1", chore("Dishes", "daily", "each", "8", "2", "3"))
	if n := count(t, h, "SELECT sum(points_awarded) FROM chore_completions"); n != 10 {
		t.Fatalf("points = %d, want 10", n)
	}
	if b := choreBoard(t, kid); strings.Count(b, " done") != 2 || strings.Contains(b, "/complete") {
		t.Fatalf("board = %q", b)
	}

	// Undo is an Admin's, and removes the completion's points.
	if !asksForPIN(t, kid, "/chores/completions/1/delete", nil) {
		t.Fatal("a locked device undid a completion")
	}
	post(t, h, "/chores/completions/1/delete", nil)
	if n := count(t, h, "SELECT sum(points_awarded) FROM chore_completions"); n != 5 {
		t.Fatalf("points after undo = %d, want 5", n)
	}
	complete(kid, t, "1", "2", "2026-03-15")
	if n := count(t, h, "SELECT points_awarded FROM chore_completions WHERE member_id = 2"); n != 8 {
		t.Fatalf("redone at %d points, want the Chore's 8", n)
	}
}

func TestAnyoneChoreIsDoneOncePerRound(t *testing.T) {
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Feed Rex", "daily", "anyone", "2", "2", "3"))
	kid := h.Device()

	complete(kid, t, "1", "3", "2026-03-15")
	complete(kid, t, "1", "2", "2026-03-15")
	if n := count(t, h, "SELECT count(*) FROM chore_completions WHERE member_id = 3"); n != 1 || count(t, h, "SELECT count(*) FROM chore_completions") != 1 {
		t.Fatal("an Anyone chore was done twice in a Round")
	}
	if b := choreBoard(t, kid); !strings.Contains(b, "Carol done") || strings.Contains(b, "/complete") {
		t.Fatalf("board = %q", b)
	}
}

func TestArchivedChoresAndMembersLeaveTheBoard(t *testing.T) {
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Dishes", "daily", "each", "5", "2", "3"))
	post(t, h, "/chores", chore("Bins", "weekly", "each", "5", "2"))
	complete(h, t, "1", "2", "2026-03-15")
	post(t, h, "/settings/members/3/archive", nil)
	post(t, h, "/chores/2/archive", nil)

	b := choreBoard(t, h)
	if strings.Contains(b, "Carol") || strings.Contains(b, "Bins") || !strings.Contains(b, "Dishes") {
		t.Fatalf("board = %q", b)
	}
	if code := complete(h, t, "1", "3", "2026-03-15"); code != http.StatusBadRequest {
		t.Fatalf("an archived Member completing = %d, want 400", code)
	}
	if code := complete(h, t, "2", "2", "2026-03-15"); code != http.StatusNotFound {
		t.Fatalf("completing an archived Chore = %d, want 404", code)
	}
	// Archived, never deleted: the Chore and its history stay.
	if count(t, h, "SELECT count(*) FROM chores") != 2 || count(t, h, "SELECT count(*) FROM chore_completions") != 1 {
		t.Fatal("archiving lost history")
	}
	post(t, h, "/chores/2/restore", nil)
	if !strings.Contains(choreBoard(t, h), "Bins") {
		t.Fatal("restored Chore isn't on the board")
	}
}

// claimableAt sets the clock to at and checks which of rounds h can still
// claim, by the board and by completing: want maps each Round to "", "Late" or
// "Missed" (Missed: refused and off the board).
func claimableAt(t *testing.T, h *apptest.Harness, chore, at string, want map[string]string) {
	t.Helper()
	now, err := time.Parse("2006-01-02 15:04", at)
	if err != nil {
		t.Fatal(err)
	}
	h.Clock.Set(now)
	_, page := h.Get(t, "/chores")
	_, b, _ := strings.Cut(page, `data-chore="`+chore+`"`)
	b, _, _ = strings.Cut(b, "</section>")
	for round, state := range want {
		_, rest, onBoard := strings.Cut(b, `data-round="`+round+`"`)
		rest, _, _ = strings.Cut(rest, "</div>")
		switch {
		case state == "Missed" && onBoard:
			t.Errorf("at %s: Missed Round %s is on the board", at, round)
		case state != "Missed" && !onBoard:
			t.Errorf("at %s: Round %s isn't on the board", at, round)
		case onBoard && strings.Contains(rest, "Late") != (state == "Late"):
			t.Errorf("at %s: Round %s Late mark wrong, want %q: %q", at, round, state, rest)
		}
	}
	for round, state := range want { // complete after checking the board, which completing changes
		code := complete(h, t, chore, "2", round)
		if (code == http.StatusConflict) != (state == "Missed") {
			t.Errorf("at %s: completing Round %s (%s) = %d", at, round, state, code)
		}
	}
	if _, err := h.App.DB.Exec("DELETE FROM chore_completions"); err != nil {
		t.Fatal(err)
	}
}

func TestLateUntilTheNextRoundEndsThenMissed(t *testing.T) {
	h := choreHousehold(t) // created Sunday 15 March 2026
	post(t, h, "/chores", chore("Daily", "daily", "each", "1", "2"))
	f := chore("Mon+Wed", "weekdays", "each", "1", "2")
	f["days"] = []string{"MO", "WE"}
	post(t, h, "/chores", f)
	post(t, h, "/chores", chore("Weekly", "weekly", "each", "1", "2"))
	post(t, h, "/chores", chore("Monthly", "monthly", "each", "1", "2"))

	t.Run("daily", func(t *testing.T) {
		claimableAt(t, h, "1", "2026-03-15 09:00", map[string]string{"2026-03-14": "Missed", "2026-03-15": "", "2026-03-16": "Missed"})
		claimableAt(t, h, "1", "2026-03-16 00:00", map[string]string{"2026-03-15": "Late", "2026-03-16": ""})
		claimableAt(t, h, "1", "2026-03-16 23:59", map[string]string{"2026-03-15": "Late", "2026-03-16": ""})
		claimableAt(t, h, "1", "2026-03-17 00:00", map[string]string{"2026-03-15": "Missed", "2026-03-16": "Late", "2026-03-17": ""})
	})
	t.Run("chosen weekdays", func(t *testing.T) {
		claimableAt(t, h, "2", "2026-03-15 09:00", map[string]string{"2026-03-11": "Missed", "2026-03-15": "Missed"})
		claimableAt(t, h, "2", "2026-03-16 09:00", map[string]string{"2026-03-16": ""})
		claimableAt(t, h, "2", "2026-03-17 09:00", map[string]string{"2026-03-16": "Late", "2026-03-17": "Missed"})
		claimableAt(t, h, "2", "2026-03-18 23:59", map[string]string{"2026-03-16": "Late", "2026-03-18": ""})
		claimableAt(t, h, "2", "2026-03-19 00:00", map[string]string{"2026-03-16": "Missed", "2026-03-18": "Late"})
	})
	t.Run("weekly", func(t *testing.T) {
		claimableAt(t, h, "3", "2026-03-21 23:59", map[string]string{"2026-03-15": ""})
		claimableAt(t, h, "3", "2026-03-28 23:59", map[string]string{"2026-03-15": "Late", "2026-03-22": ""})
		claimableAt(t, h, "3", "2026-03-29 00:00", map[string]string{"2026-03-15": "Missed", "2026-03-22": "Late", "2026-03-29": ""})
	})
	t.Run("monthly", func(t *testing.T) {
		claimableAt(t, h, "4", "2026-03-31 23:59", map[string]string{"2026-03-01": ""})
		claimableAt(t, h, "4", "2026-04-30 23:59", map[string]string{"2026-03-01": "Late", "2026-04-01": ""})
		claimableAt(t, h, "4", "2026-05-01 00:00", map[string]string{"2026-03-01": "Missed", "2026-04-01": "Late", "2026-05-01": ""})
	})
}

func TestAnAnyoneChoreCanBeClaimedLateOnlyIfNobodyDidThatRound(t *testing.T) {
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Feed Rex", "daily", "anyone", "2", "2", "3"))
	complete(h, t, "1", "3", "2026-03-15")
	h.Clock.Set(time.Date(2026, 3, 16, 8, 0, 0, 0, time.UTC))
	if b := choreBoard(t, h); strings.Contains(b, `data-round="2026-03-15"`) {
		t.Fatalf("a done Round shows as Late: %q", b)
	}
	complete(h, t, "1", "2", "2026-03-15")
	if n := count(t, h, "SELECT count(*) FROM chore_completions"); n != 1 {
		t.Fatalf("completions = %d, want 1", n)
	}
}

func TestChoreLockGatesCompleting(t *testing.T) {
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Dishes", "daily", "each", "5", "2"))
	kid := h.Device()
	if asksForPIN(t, kid, "/chores/1/complete", url.Values{"member_id": {"2"}, "round": {"2026-03-15"}}) {
		t.Fatal("completing asked for a PIN with the chore Lock off")
	}
	h.App.DB.Exec("DELETE FROM chore_completions")
	post(t, h, "/settings/locks", url.Values{"lock_chores": {"on"}})
	if _, body := h.Get(t, "/settings"); !strings.Contains(body, `name="lock_chores" checked`) {
		t.Fatal("Settings doesn't show the chore Lock on")
	}
	if !asksForPIN(t, kid, "/chores/1/complete", url.Values{"member_id": {"2"}, "round": {"2026-03-15"}}) {
		t.Fatal("completing a locked Chore did not ask for a PIN")
	}
	if b := choreBoard(t, kid); !strings.Contains(b, "Dishes") {
		t.Fatal("viewing chores was locked")
	}
	unlock(t, kid, 1, "1234")
	complete(kid, t, "1", "2", "2026-03-15")
	if count(t, h, "SELECT count(*) FROM chore_completions WHERE member_id = 2") != 1 {
		t.Fatal("an unlocked device could not complete, crediting the tapped Member")
	}
}

func TestTodaysChoresAreOnTodayButNeverOnTheCalendar(t *testing.T) {
	h := choreHousehold(t) // Sunday 15 March
	post(t, h, "/chores", chore("Dishes", "daily", "each", "5", "2"))
	f := chore("Bins", "weekdays", "each", "1", "2")
	f["days"] = []string{"MO"}
	post(t, h, "/chores", f)

	_, page := h.Get(t, "/today")
	_, chores, _ := strings.Cut(page, `id="today-chores"`)
	chores, _, _ = strings.Cut(chores, "</section>")
	if !strings.Contains(chores, "Dishes") || strings.Contains(chores, "Bins") || !strings.Contains(chores, `value="/today"`) {
		t.Fatalf("Today's Chores = %q", chores)
	}
	if code, _ := h.Post(t, "/chores/1/complete", url.Values{"member_id": {"2"}, "round": {"2026-03-15"}, "next": {"/today"}}); code != http.StatusOK {
		t.Fatal(code)
	}
	if _, page := h.Get(t, "/today"); !strings.Contains(page, "Bob done") {
		t.Fatal("Today doesn't show the completion")
	}
	for _, path := range []string{"/calendar?date=2026-03-15", "/calendar?view=week&date=2026-03-15", "/calendar?view=month&date=2026-03-15"} {
		if _, page := h.Get(t, path); strings.Contains(page, "Dishes") {
			t.Errorf("%s shows a Chore", path)
		}
	}
}
