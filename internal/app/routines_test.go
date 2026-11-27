package app_test

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/apptest"
)

// routines is the Routine builder's form: each Routine's Chore ids in order.
func routines(morning, afternoon, evening []string) url.Values {
	return url.Values{"morning": morning, "afternoon": afternoon, "evening": evening}
}

// builder is the Routine builder pop-up on the chore board.
func builder(t *testing.T, page string) string {
	t.Helper()
	_, b, ok := strings.Cut(page, `id="routine-builder"`)
	if !ok {
		t.Fatalf("no Routine builder on %q", page)
	}
	return b
}

var builderList = regexp.MustCompile(`(?s)data-routine-list="(\w+)".*?</ul>`)
var builderItem = regexp.MustCompile(`data-routine-chore="(\d+)"`)

// routineOrder is the Chore ids in each of the builder's lists, in order.
func routineOrder(t *testing.T, h *apptest.Harness) map[string][]string {
	t.Helper()
	_, page := h.Get(t, "/chores")
	got := map[string][]string{}
	for _, l := range builderList.FindAllStringSubmatch(builder(t, page), -1) {
		for _, it := range builderItem.FindAllStringSubmatch(l[0], -1) {
			got[l[1]] = append(got[l[1]], it[1])
		}
	}
	return got
}

// slotTag is the slot tag the chore board shows on Chore id, or "".
func slotTag(t *testing.T, board, id string) string {
	t.Helper()
	_, card, ok := strings.Cut(board, `data-chore="`+id+`"`)
	if !ok {
		t.Fatalf("Chore %s not on the board %q", id, board)
	}
	card, _, _ = strings.Cut(card, "</section>")
	if m := regexp.MustCompile(`data-slot="(\w+)"`).FindStringSubmatch(card); m != nil {
		return m[1]
	}
	return ""
}

func TestOnlyDailyEachChoresGoInARoutineInTheBuildersOrder(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)                                                     // Sunday 15 March 2026, 09:00
	post(t, h, "/chores", chore("Dishes", "daily", "each", "5", "2"))          // 1
	teeth := chore("Teeth", "weekdays", "each", "1", "2", "3")                 //
	teeth["days"] = []string{"MO", "SU"}                                       //
	post(t, h, "/chores", teeth)                                               // 2
	post(t, h, "/chores", chore("Bins", "weekly", "each", "3", "2"))           // 3
	post(t, h, "/chores", chore("Hoover", "monthly", "each", "3", "2"))        // 4
	post(t, h, "/chores", chore("Feed the fish", "daily", "anyone", "1", "3")) // 5

	if !asksForPIN(t, h.Device(), "/chores/routines", routines([]string{"1"}, nil, nil)) {
		t.Fatal("a locked device changed the Routines")
	}
	for _, bad := range []url.Values{
		routines([]string{"3"}, nil, nil),           // weekly
		routines(nil, []string{"4"}, nil),           // monthly
		routines(nil, nil, []string{"5"}),           // an Anyone chore
		routines([]string{"1"}, nil, []string{"1"}), // two Routines at once
		routines([]string{"99"}, nil, nil),          // no such Chore
	} {
		if code, _ := h.Post(t, "/chores/routines", bad); code != http.StatusBadRequest {
			t.Fatalf("Routines %v = %d, want 400", bad, code)
		}
	}
	post(t, h, "/chores/routines", routines([]string{"2", "1"}, nil, nil))

	got := routineOrder(t, h)
	if strings.Join(got["morning"], ",") != "2,1" || len(got["afternoon"])+len(got["evening"])+len(got["none"]) != 0 {
		t.Fatalf("builder lists = %v, want Teeth then Dishes in Morning and nothing else eligible", got)
	}
	post(t, h, "/chores/routines", routines([]string{"1"}, nil, []string{"2"}))
	if got := routineOrder(t, h); strings.Join(got["morning"], ",") != "1" || strings.Join(got["evening"], ",") != "2" {
		t.Fatalf("builder lists after moving Teeth = %v", got)
	}

	// The board only gains a slot tag; a Morning Chore isn't Late in the afternoon.
	h.Clock.Set(time.Date(2026, 3, 15, 18, 0, 0, 0, time.UTC))
	b := choreBoard(t, h.Device())
	if slotTag(t, b, "1") != "morning" || slotTag(t, b, "2") != "evening" || slotTag(t, b, "3") != "" || slotTag(t, b, "5") != "" {
		t.Fatalf("slot tags on %q", b)
	}
	if strings.Contains(b, "Late") {
		t.Fatalf("a Routine made a Chore Late: %q", b)
	}
	if !strings.Contains(b, ">Morning<") || !strings.Contains(b, ">Evening<") {
		t.Fatalf("slot tags aren't named: %q", b)
	}

	// The builder shows a locked device the Routines, not a way to change them.
	_, locked := h.Device().Get(t, "/chores")
	if bl := builder(t, locked); strings.Contains(bl, `action="/chores/routines"`) || !strings.Contains(bl, "Unlock") {
		t.Fatalf("locked builder = %q", bl)
	}
}

func TestAChoreLeavesItsRoutineWhenItStopsBeingEligibleOrIsArchived(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Dishes", "daily", "each", "5", "2")) // 1
	post(t, h, "/chores", chore("Teeth", "daily", "each", "1", "2"))  // 2
	post(t, h, "/chores", chore("Bath", "daily", "each", "1", "2"))   // 3
	post(t, h, "/chores", chore("Read", "daily", "each", "1", "2"))   // 4
	post(t, h, "/chores/routines", routines([]string{"1", "2"}, []string{"3"}, []string{"4"}))

	post(t, h, "/chores/1", chore("Dishes", "daily", "each", "8", "2", "3")) // still eligible
	post(t, h, "/chores/2", chore("Teeth", "weekly", "each", "1", "2"))
	post(t, h, "/chores/3", chore("Bath", "daily", "anyone", "1", "2"))
	post(t, h, "/chores/4/archive", nil)
	post(t, h, "/chores/4/restore", nil)

	b := choreBoard(t, h)
	for id, want := range map[string]string{"1": "morning", "2": "", "3": "", "4": ""} {
		if got := slotTag(t, b, id); got != want {
			t.Fatalf("Chore %s slot = %q, want %q", id, got, want)
		}
	}
	// Back to daily Each, Teeth is eligible again but in no Routine until put back.
	post(t, h, "/chores/2", chore("Teeth", "daily", "each", "1", "2"))
	got := routineOrder(t, h)
	if strings.Join(got["morning"], ",") != "1" || strings.Join(got["none"], ",") != "4,2" && strings.Join(got["none"], ",") != "2,4" || len(got["afternoon"])+len(got["evening"]) != 0 {
		t.Fatalf("builder lists = %v", got)
	}
}

func TestSlotTimesAreAHouseholdSetting(t *testing.T) {
	t.Parallel()
	h := household(t)
	_, page := h.Get(t, "/settings")
	if !strings.Contains(page, `name="morning_end" value="12:00"`) || !strings.Contains(page, `name="afternoon_end" value="17:00"`) {
		t.Fatalf("settings don't show the default slot times: %q", page)
	}
	base := url.Values{"default_view": {"today"}, "week_start": {"0"}}
	with := func(m, a string) url.Values {
		f := url.Values{"morning_end": {m}, "afternoon_end": {a}}
		for k, v := range base {
			f[k] = v
		}
		return f
	}
	for _, bad := range []url.Values{with("17:00", "12:00"), with("12:00", "12:00"), with("noon", "17:00"), with("00:00", "17:00")} {
		if code, _ := h.Post(t, "/settings/household", bad); code != http.StatusBadRequest {
			t.Fatalf("slot times %v = %d, want 400", bad, code)
		}
	}
	if !asksForPIN(t, h.Device(), "/settings/household", with("11:30", "16:00")) {
		t.Fatal("a locked device changed the slot times")
	}
	post(t, h, "/settings/household", with("11:30", "16:00"))
	post(t, h, "/settings/household", base) // other Household settings keep them
	_, page = h.Get(t, "/settings")
	if !strings.Contains(page, `name="morning_end" value="11:30"`) || !strings.Contains(page, `name="afternoon_end" value="16:00"`) {
		t.Fatalf("settings don't show the saved slot times: %q", page)
	}
}
