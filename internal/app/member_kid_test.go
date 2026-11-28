package app_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/DeRusto/goCasa/internal/apptest"
)

// kidStyle switches Member id's page to Kid style from Settings → Members.
func kidStyle(t *testing.T, h *apptest.Harness, id, name, color string) {
	t.Helper()
	f := member(name, color, "member", "")
	f.Set("style", "kid")
	post(t, h, "/settings/members/"+id, f)
}

// kidPage is Member id's page, which must render in Kid style.
func kidPage(t *testing.T, h *apptest.Harness, id string) string {
	t.Helper()
	page := memberPage(t, h, id)
	if !strings.Contains(page, `data-style="kid"`) {
		t.Fatalf("Member %s's page isn't in Kid style: %q", id, page)
	}
	return page
}

// memberFlyout is Member id's edit flyout in Settings.
func memberFlyout(t *testing.T, h *apptest.Harness, id string) string {
	t.Helper()
	_, page := h.Get(t, "/settings")
	_, s, ok := strings.Cut(page, `id="member-`+id+`"`)
	if !ok {
		t.Fatalf("no flyout for Member %s in Settings", id)
	}
	s, _, _ = strings.Cut(s, "</form>")
	return s
}

// The style is a choice on each person's row in Settings, Standard until an
// Admin picks Kid; pets have none. It's the Member's, so every device shows it.
func TestAMembersStyleIsChosenInSettingsAndShownOnEveryDevice(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t) // Rex (4) is a pet
	bob := memberFlyout(t, h, "2")
	if !strings.Contains(bob, `name="style"`) || !strings.Contains(bob, `<option value="standard" selected`) || !strings.Contains(bob, `value="kid"`) {
		t.Fatalf("Bob's row has no Standard/Kid choice set to Standard: %q", bob)
	}
	if strings.Contains(memberFlyout(t, h, "4"), `name="style"`) {
		t.Error("the pet Rex has a style choice")
	}
	if page := memberPage(t, h, "2"); strings.Contains(page, `data-style="kid"`) || !strings.Contains(page, `id="member-chores"`) {
		t.Fatal("a new Member's page isn't in Standard style")
	}

	kidStyle(t, h, "2", "Bob", "#22c55e")
	if !strings.Contains(memberFlyout(t, h, "2"), `<option value="kid" selected`) {
		t.Error("Settings doesn't show Bob's Kid style as chosen")
	}
	kidPage(t, h, "2")
	kidPage(t, h.Device(), "2") // another device, never unlocked
	if strings.Contains(memberPage(t, h, "3"), `data-style="kid"`) {
		t.Error("Bob's style changed Carol's page")
	}

	// Saving the row again without the field (a pet's form) keeps the style.
	post(t, h, "/settings/members/2", member("Bob", "#22c55e", "member", ""))
	kidPage(t, h, "2")
	f := member("Bob", "#22c55e", "member", "")
	f.Set("style", "fancy")
	if code, _ := h.Post(t, "/settings/members/2", f); code != http.StatusBadRequest {
		t.Errorf("an unknown style = %d, want 400", code)
	}
	f.Set("style", "standard")
	post(t, h, "/settings/members/2", f)
	if strings.Contains(memberPage(t, h, "2"), `data-style="kid"`) {
		t.Error("switching Bob back to Standard didn't")
	}

	f = member("Dan", "#0ea5e9", "member", "")
	f.Set("style", "kid")
	post(t, h, "/settings/members", f) // 5
	kidPage(t, h, "5")
}

// The top row: avatar and "Hi {name}!" with the day, a text clock without
// am/pm in 12-hour, and the balance with the cheapest Reward out of reach.
func TestKidStyleTopRowGreetsWithTheClockAndPointsGoal(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t) // Sunday 15 March 2026, 09:00
	kidStyle(t, h, "2", "Bob", "#22c55e")
	post(t, h, "/adjustments", url.Values{"member_id": {"2"}, "points": {"37"}, "reason": {"Start"}})
	post(t, h, "/rewards", url.Values{"name": {"Ice cream"}, "points": {"20"}})
	post(t, h, "/rewards", url.Values{"name": {"Stay up late"}, "points": {"80"}})
	post(t, h, "/rewards", url.Values{"name": {"Movie night"}, "points": {"50"}})

	page := kidPage(t, h, "2")
	for _, want := range []string{"Hi Bob!", "Sunday", `class="avatar`, `data-balance="2">37<`, "13 more to Movie night", `id="clock"`, `>09:00</time>`} {
		if !strings.Contains(page, want) {
			t.Errorf("Kid style's top row lacks %q", want)
		}
	}
	if strings.Contains(page, "<svg class=\"clockface") {
		t.Error("Kid style draws a clock face")
	}

	post(t, h, "/settings/household", url.Values{"default_view": {"today"}, "week_start": {"0"}}) // 12-hour
	page = kidPage(t, h, "2")
	if !strings.Contains(page, `>9:00</time>`) || !strings.Contains(page, `data-ampm="false"`) {
		t.Errorf("a 12-hour Kid clock isn't plain 9:00: %q", page)
	}

	post(t, h, "/adjustments", url.Values{"member_id": {"2"}, "points": {"50"}, "reason": {"More"}})
	if page := kidPage(t, h, "2"); strings.Contains(page, "more to") || !strings.Contains(page, "You can get any Reward!") {
		t.Error("with every Reward in reach the goal line still counts down")
	}
}

// tile is the Kid style tile for routine.
func tile(t *testing.T, page, routine string) string {
	t.Helper()
	_, s, ok := strings.Cut(page, `data-tile="`+routine+`"`)
	if !ok {
		t.Fatalf("no %s tile in %q", routine, page)
	}
	s, _, _ = strings.Cut(s, "data-tile=")
	return s
}

// Four tiles in a row; the current Routine's is twice as wide in the
// Member's colour; each has its emoji, name, a dot per Chore and its progress.
func TestKidStyleShowsFourRoutineTilesWithProgress(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)                                                 // 09:00: Morning
	post(t, h, "/chores", chore("Teeth", "daily", "each", "1", "2"))       // 1
	post(t, h, "/chores", chore("Get dressed", "daily", "each", "1", "2")) // 2
	post(t, h, "/chores", chore("Homework", "daily", "each", "2", "2"))    // 3
	post(t, h, "/chores", chore("Bins", "weekly", "each", "3", "2"))       // 4
	post(t, h, "/chores/routines", routines([]string{"1", "2"}, []string{"3"}, nil))
	kidStyle(t, h, "2", "Bob", "#22c55e")

	page := kidPage(t, h, "2")
	if !inOrder(page, `data-tile="morning"`, `data-tile="afternoon"`, `data-tile="evening"`, `data-tile="other"`) {
		t.Fatal("the tiles aren't Morning, Afternoon, Evening, Other chores")
	}
	for rt, want := range map[string][]string{
		"morning":   {"🌅", ">Morning<", "0 of 2"},
		"afternoon": {"🎒", ">Afternoon<", "0 of 1"},
		"evening":   {"🌙", ">Evening<"},
		"other":     {"⭐", ">Other chores<", "0 of 1"},
	} {
		for _, w := range want {
			if !strings.Contains(tile(t, page, rt), w) {
				t.Errorf("the %s tile lacks %q: %s", rt, w, tile(t, page, rt))
			}
		}
	}
	m := tile(t, page, "morning")
	if !strings.Contains(m, "data-current") || !strings.Contains(m, "flex:2") || !strings.Contains(m, "background:#22c55e") {
		t.Errorf("the current Morning tile isn't wide in Bob's colour: %s", m)
	}
	if a := tile(t, page, "afternoon"); strings.Contains(a, "data-current") || strings.Contains(a, "flex:2") {
		t.Errorf("the Afternoon tile is drawn as current: %s", a)
	}
	if n := strings.Count(m, `data-dot="todo"`); n != 2 {
		t.Errorf("Morning has %d open dots, want 2", n)
	}

	complete(h, t, "1", "2", "2026-03-15")
	m = tile(t, kidPage(t, h, "2"), "morning")
	if !strings.Contains(m, "1 of 2") || strings.Count(m, `data-dot="done"`) != 1 || strings.Count(m, `data-dot="todo"`) != 1 {
		t.Errorf("one tick isn't one green dot of two: %s", m)
	}
	complete(h, t, "3", "2", "2026-03-15")
	if a := tile(t, kidPage(t, h, "2"), "afternoon"); !strings.Contains(a, "All done!") || strings.Contains(a, "1 of 1") {
		t.Errorf("a finished Afternoon doesn't say All done!: %s", a)
	}
}

// Under the chores Lock a provisional tick counts in the tile, and the tile
// offers the same Confirm as Standard style.
func TestKidStyleTilesCountProvisionalTicksAndOfferConfirm(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Teeth", "daily", "each", "5", "2"))
	post(t, h, "/chores", chore("Get dressed", "daily", "each", "3", "2"))
	post(t, h, "/chores/routines", routines([]string{"1", "2"}, nil, nil))
	post(t, h, "/settings/locks", url.Values{"lock_chores": {"on"}})
	kidStyle(t, h, "2", "Bob", "#22c55e")
	kid := h.Device()
	if m := tile(t, kidPage(t, kid, "2"), "morning"); strings.Contains(m, "Confirm") {
		t.Errorf("Morning offers Confirm with nothing to confirm: %s", m)
	}
	kid.Post(t, "/members/2/routine-chores/1", url.Values{"round": {"2026-03-15"}})
	m := tile(t, kidPage(t, kid, "2"), "morning")
	if !strings.Contains(m, "1 of 2") || strings.Count(m, `data-dot="done"`) != 1 {
		t.Errorf("the provisional tick doesn't count in the tile: %s", m)
	}
	if !strings.Contains(m, `href="/members/2/routines/morning/confirm"`) || !strings.Contains(m, "Confirm") {
		t.Errorf("Morning doesn't offer Confirm: %s", m)
	}
}

// The strip under the tiles: the next event with +N opening the rest of
// today's, in plain times, then the Member's to-dos as chips that tick.
func TestKidStyleStripShowsTheNextEventAndTicksToDos(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)                                                                        // 09:00
	post(t, h, "/settings/household", url.Values{"default_view": {"today"}, "week_start": {"0"}}) // 12-hour
	addEvent(t, h, timed("Breakfast", "2026-03-15T07:00", "2026-03-15T08:00", "UTC", "2"))        // over
	addEvent(t, h, timed("Swim lesson", "2026-03-15T15:30", "2026-03-15T16:30", "UTC", "2"))
	addEvent(t, h, timed("Dinner", "2026-03-15T18:00", "2026-03-15T19:00", "UTC"))
	addEvent(t, h, timed("Football", "2026-03-15T10:00", "2026-03-15T11:00", "UTC", "2"))
	addEvent(t, h, timed("Piano", "2026-03-15T12:00", "2026-03-15T13:00", "UTC", "3")) // Carol's
	post(t, h, "/todo-columns", url.Values{"name": {"Bob's jobs"}})
	columnOf(t, h, "1", "Bob's jobs", "2")
	post(t, h, "/todos", todo("1", "Library book back", ""))
	kidStyle(t, h, "2", "Bob", "#22c55e")

	page := kidPage(t, h, "2")
	strip := section(t, page, "kid-strip")
	next, _, _ := strings.Cut(strip, `popovertarget="kid-events"`)
	if !strings.Contains(next, "Football") || !strings.Contains(next, ">10:00<") || strings.Contains(next, "Swim") {
		t.Errorf("the next event isn't Football at 10:00: %q", next)
	}
	if !strings.Contains(strip, "+3") {
		t.Errorf("the strip doesn't offer +3 more events: %q", strip)
	}
	if strings.Contains(strip, "Piano") || strings.Contains(strip, " PM") || strings.Contains(strip, " AM") {
		t.Errorf("the strip shows Carol's event or am/pm: %q", strip)
	}
	rest := section(t, page, "kid-events")
	if !inOrder(rest, "7:00", "Breakfast", "3:30", "Swim lesson", "6:00", "Dinner") || strings.Contains(rest, "Football") {
		t.Errorf("+3 doesn't open the rest of today's events: %q", rest)
	}

	if !strings.Contains(strip, "Library book back") || !strings.Contains(strip, `action="/todos/1/complete"`) || !strings.Contains(strip, `name="next" value="/members/2"`) {
		t.Fatalf("the to-do isn't a chip that ticks: %q", strip)
	}
	code, page := h.Post(t, "/todos/1/complete", url.Values{"next": {"/members/2"}})
	if code != http.StatusOK || !strings.Contains(page, `data-style="kid"`) || strings.Contains(section(t, page, "kid-strip"), "Library book back") {
		t.Errorf("ticking the to-do chip = %d, still showing it or not back on Bob's page", code)
	}
}

// Tapping the star opens the Rewards: affordable ones with "I want it",
// requested ones waiting, each with its emoji or 🎁.
func TestKidStyleRewardsPanelRequestsRewards(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	kidStyle(t, h, "2", "Bob", "#22c55e")
	post(t, h, "/adjustments", url.Values{"member_id": {"2"}, "points": {"30"}, "reason": {"Start"}})
	post(t, h, "/rewards", url.Values{"name": {"Ice cream"}, "points": {"20"}, "emoji": {"🍦"}}) // 1
	post(t, h, "/rewards", url.Values{"name": {"Sticker"}, "points": {"5"}})                    // 2
	post(t, h, "/rewards", url.Values{"name": {"Bike"}, "points": {"500"}})                     // 3

	page := kidPage(t, h, "2")
	if !strings.Contains(page, `popovertarget="kid-rewards"`) {
		t.Fatal("the star doesn't open the Rewards")
	}
	panel := section(t, page, "kid-rewards")
	for _, want := range []string{"🍦", "Ice cream", "🎁", "Sticker", "Bike", `action="/rewards/1/redeem"`, `action="/rewards/2/redeem"`, "I want it"} {
		if !strings.Contains(panel, want) {
			t.Errorf("the Rewards panel lacks %q: %q", want, panel)
		}
	}
	if strings.Contains(panel, `action="/rewards/3/redeem"`) {
		t.Error("the Bike, out of reach, can be asked for")
	}
	if strings.Count(panel, "data-affordable") != 2 {
		t.Errorf("the Rewards highlighted aren't the two affordable ones: %q", panel)
	}

	code, page := h.Device().Post(t, "/rewards/1/redeem", url.Values{"member_id": {"2"}, "next": {"/members/2"}})
	if code != http.StatusOK || !strings.Contains(page, `data-style="kid"`) {
		t.Fatalf("asking for Ice cream = %d, not back on Bob's page", code)
	}
	panel = section(t, page, "kid-rewards")
	ice, _, _ := strings.Cut(panel[strings.Index(panel, "Ice cream"):], "Bike")
	if !strings.Contains(ice, "waiting…") || strings.Contains(ice, "I want it") {
		t.Errorf("the requested Ice cream isn't waiting: %q", ice)
	}
}

// Tapping the weather opens a small view: now, high and low, the chance of
// rain and the next few hours.
func TestKidStyleWeatherViewShowsTheForecast(t *testing.T) {
	t.Parallel()
	h, m := weatherHousehold(t)
	post(t, h, "/settings/members", member("Bob", "#22c55e", "member", "")) // 2
	kidStyle(t, h, "2", "Bob", "#22c55e")
	post(t, h, "/settings/weather", url.Values{"lat": {"51.5"}, "lon": {"-0.12"}, "units": {"metric"}})
	h.RunDue()
	m.Close()

	page := kidPage(t, h, "2")
	if !strings.Contains(page, `popovertarget="kid-weather"`) {
		t.Fatal("the weather doesn't open its view")
	}
	view := section(t, page, "kid-weather")
	for _, want := range []string{"12°", "H 16°", "L 8°", "Chance of rain", "09:00", "10:00", "60%"} {
		if !strings.Contains(view, want) {
			t.Errorf("the weather view lacks %q: %q", want, view)
		}
	}
}
