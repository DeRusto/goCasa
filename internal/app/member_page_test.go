package app_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/apptest"
)

const notAvailable = "This Member page isn't available. Ask an Admin to choose another Member."

// memberPage is Member id's page, which must render.
func memberPage(t *testing.T, h *apptest.Harness, id string) string {
	t.Helper()
	code, page := h.Get(t, "/members/"+id)
	if code != http.StatusOK || !strings.Contains(page, `data-member-page="`+id+`"`) {
		t.Fatalf("GET /members/%s = %d %q", id, code, page)
	}
	return page
}

// section is the part of page from the element with id to its closing </section>.
func section(t *testing.T, page, id string) string {
	t.Helper()
	_, s, ok := strings.Cut(page, `id="`+id+`"`)
	if !ok {
		t.Fatalf("no #%s in %q", id, page)
	}
	s, _, _ = strings.Cut(s, "</section>")
	return s
}

// The events are eventsIn's Member filter: the Member's own and Household
// events, not other Members'; Standard style adds tomorrow's first event.
func TestMemberPageShowsOwnAndHouseholdEventsAndTomorrowsFirst(t *testing.T) {
	t.Parallel()
	h := household(t)                                                                     // Sunday 15 March 2026, 09:00 UTC
	addEvent(t, h, timed("Dinner", "2026-03-15T18:00", "2026-03-15T19:00", "UTC"))        // Household
	addEvent(t, h, timed("Football", "2026-03-15T10:00", "2026-03-15T11:00", "UTC", "2")) // Bob
	addEvent(t, h, timed("Piano", "2026-03-15T12:00", "2026-03-15T13:00", "UTC", "3"))    // Carol
	addEvent(t, h, timed("Early run", "2026-03-16T07:00", "2026-03-16T08:00", "UTC", "3"))
	addEvent(t, h, timed("Swim", "2026-03-16T10:00", "2026-03-16T11:00", "UTC", "2"))
	addEvent(t, h, timed("School", "2026-03-16T08:30", "2026-03-16T15:00", "UTC", "2", "3"))
	addEvent(t, h, timed("Too far", "2026-03-17T08:00", "2026-03-17T09:00", "UTC", "2"))

	page := memberPage(t, h, "2")
	today := day(t, page, "2026-03-15")
	for _, title := range []string{"Dinner", "Football"} {
		if !strings.Contains(today, title) {
			t.Errorf("Bob's today lacks %s: %q", title, today)
		}
	}
	if strings.Contains(page, "Piano") || strings.Contains(page, "Early run") {
		t.Errorf("Bob's page shows Carol's events: %q", page)
	}
	peek := section(t, page, "member-tomorrow")
	if !strings.Contains(peek, "08:30") || !strings.Contains(peek, "School") || strings.Contains(peek, "Swim") {
		t.Errorf("tomorrow's peek isn't Bob's first event tomorrow: %q", peek)
	}
	if strings.Contains(page, "Too far") {
		t.Error("the page shows a day after tomorrow")
	}

	if !strings.Contains(memberPage(t, h, "3"), "Piano") {
		t.Error("Carol's page lacks her event")
	}
	if _, quiet := h.Get(t, "/members/1"); !strings.Contains(section(t, quiet, "member-tomorrow"), "Nothing tomorrow") {
		t.Errorf("Ada's peek with nothing tomorrow = %q", section(t, quiet, "member-tomorrow"))
	}
}

// Like Today: the normal layout, live refresh and the day turning at midnight.
func TestMemberPageUsesTheLayoutAndKeepsItselfCurrent(t *testing.T) {
	t.Parallel()
	h := household(t)
	page := memberPage(t, h, "2")
	for _, want := range []string{`href="/today"`, `href="/settings"`, "/events/stream", `id="live"`, `id="clock"`, `data-midnight-in="54000"`, "Sunday 15 March", "Bob"} {
		if !strings.Contains(page, want) {
			t.Errorf("the Member page lacks %s", want)
		}
	}
	for _, kind := range []string{"sse:event", "sse:chore", "sse:points", "sse:member", "sse:household", "sse:integration"} {
		if !strings.Contains(page, kind) {
			t.Errorf("the Member page doesn't refresh on %s", kind)
		}
	}
}

func TestMemberPageShowsTheWeather(t *testing.T) {
	t.Parallel()
	h, m := weatherHousehold(t)
	post(t, h, "/settings/weather", url.Values{"lat": {"51.5"}, "lon": {"-0.12"}, "units": {"metric"}})
	h.RunDue()
	m.Close() // from the cache alone
	page := memberPage(t, h, "1")
	for _, want := range []string{"12°C", "Overcast", "H 16° L 8°"} {
		if !strings.Contains(page, want) {
			t.Errorf("the Member page lacks %q", want)
		}
	}
}

func TestMemberPageIsNotAvailableForArchivedDeletedUnknownOrPetMembers(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t) // Rex (4) is a pet
	post(t, h, "/settings/members", member("Dan", "#0ea5e9", "member", ""))
	post(t, h, "/settings/members/5/delete", nil)
	post(t, h, "/settings/members/3/archive", nil)
	for _, id := range []string{"3", "4", "5", "99", "abc"} {
		code, page := h.Get(t, "/members/"+id)
		if code != http.StatusNotFound || !strings.Contains(page, notAvailable) {
			t.Errorf("GET /members/%s = %d %q", id, code, page)
		}
		if strings.Contains(page, "<nav") || strings.Contains(page, "data-member-page") {
			t.Errorf("/members/%s isn't a plain page: %q", id, page)
		}
	}
	if strings.Contains(memberPage(t, h, "2"), notAvailable) {
		t.Error("an active Member's page says it isn't available")
	}
}

// The page lists this Member's Chores for today, done or not, and ticking one
// goes through completeChore for this Member and comes back.
func TestMemberPageListsTheMembersChoresAndTicksThem(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Dishes", "daily", "each", "5", "2", "3"))
	post(t, h, "/chores", chore("Feed fish", "daily", "anyone", "2", "2", "3"))
	post(t, h, "/chores", chore("Piano practice", "daily", "each", "1", "3"))
	post(t, h, "/chores", chore("Bins", "weekly", "each", "3", "2"))

	cs := section(t, memberPage(t, h, "2"), "member-chores")
	for _, want := range []string{"Dishes", "Feed fish", "Bins", `aria-label="Complete Dishes for Bob"`} {
		if !strings.Contains(cs, want) {
			t.Errorf("Bob's chores lack %s: %q", want, cs)
		}
	}
	if strings.Contains(cs, "Piano practice") || strings.Contains(cs, "for Carol") {
		t.Errorf("Bob's chores show Carol's: %q", cs)
	}

	code, page := h.Post(t, "/chores/1/complete", url.Values{"member_id": {"2"}, "round": {"2026-03-15"}, "next": {"/members/2"}})
	if code != http.StatusOK || !strings.Contains(page, `data-member-page="2"`) {
		t.Fatalf("ticking returned %d to %q, want Bob's page", code, page)
	}
	if cs := section(t, page, "member-chores"); !strings.Contains(cs, "Bob done") || strings.Contains(cs, "Complete Dishes for Bob") {
		t.Errorf("Dishes not ticked on Bob's page: %q", cs)
	}
	if got := balance(t, h, "2"); got != "5" {
		t.Errorf("Bob's balance after ticking = %s, want 5", got)
	}
	if !strings.Contains(section(t, memberPage(t, h, "3"), "member-chores"), "Complete Dishes for Carol") {
		t.Error("Bob's tick ticked Carol's Dishes")
	}

	complete(h, t, "2", "3", "2026-03-15") // Carol feeds the fish for everyone
	if cs := section(t, memberPage(t, h, "2"), "member-chores"); !strings.Contains(cs, "Carol done") || strings.Contains(cs, "Complete Feed fish for Bob") {
		t.Errorf("the Anyone chore Carol did isn't done on Bob's page: %q", cs)
	}

	h.Clock.Advance(24 * time.Hour) // Monday: Carol's Sunday Dishes are Late
	unlock(t, h, 1, "1234")
	if cs := section(t, memberPage(t, h, "3"), "member-chores"); !strings.Contains(cs, "Late") {
		t.Errorf("Carol's Late Round has no Late tag: %q", cs)
	}
	// Bob did his Sunday Dishes; while Carol still can, the Round stays on his page, done, as on Today.
	if cs := section(t, memberPage(t, h, "2"), "member-chores"); strings.Count(cs, `data-round="2026-03-15"`) != 2 { // Dishes and the weekly Bins
		t.Errorf("Bob's ticked Late Round left his page: %q", cs)
	}
}

func TestTickingFromTheMemberPageFollowsTheChoresLock(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Dishes", "daily", "each", "5", "2"))
	post(t, h, "/settings/locks", url.Values{"lock_chores": {"on"}})
	kid := h.Device()
	body := postFrom(t, kid, "/members/2", "/chores/1/complete", url.Values{"member_id": {"2"}, "round": {"2026-03-15"}, "next": {"/members/2"}})
	if !strings.Contains(body, lockPage) || !strings.Contains(body, `name="next" value="/members/2"`) {
		t.Fatalf("a locked tick didn't ask for the PIN and come back: %q", body)
	}
	if n := count(t, h, "SELECT count(*) FROM chore_completions"); n != 0 {
		t.Fatalf("a locked tick counted: %d completions", n)
	}
}

// A next that isn't a Member page sends the person to the chore board.
func TestChoreActionsReturnOnlyToAMemberPagePath(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Dishes", "daily", "each", "5", "2"))
	h.Client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for next, want := range map[string]string{"/members/2": "/members/2", "/members/2/avatar": "/chores", "//evil.example": "/chores", "/members/x": "/chores"} {
		h.App.DB.Exec("DELETE FROM chore_completions")
		req, _ := http.NewRequest(http.MethodPost, h.Server.URL+"/chores/1/complete", strings.NewReader(url.Values{"member_id": {"2"}, "round": {"2026-03-15"}, "next": {next}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
		res, err := h.Client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if loc := res.Header.Get("Location"); loc != want {
			t.Errorf("next=%q went to %q, want %q", next, loc, want)
		}
	}
}

// Tested with two Members on different balances: each page shows its own
// balance, affordable Rewards and pending Redemptions.
func TestMemberPageShowsTheMembersPointsAndRequestsRewards(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/adjustments", url.Values{"member_id": {"2"}, "points": {"10"}, "reason": {"Start"}})
	post(t, h, "/adjustments", url.Values{"member_id": {"3"}, "points": {"2"}, "reason": {"Start"}})
	post(t, h, "/rewards", url.Values{"name": {"Ice cream"}, "points": {"3"}})
	post(t, h, "/rewards", url.Values{"name": {"Bike"}, "points": {"50"}})

	bob := section(t, memberPage(t, h, "2"), "member-points")
	if !strings.Contains(bob, `data-balance="2">10<`) {
		t.Errorf("Bob's balance isn't 10: %q", bob)
	}
	if !strings.Contains(bob, `aria-label="Ask for Ice cream"`) || strings.Contains(bob, `aria-label="Ask for Bike"`) {
		t.Errorf("Bob's affordable Rewards = %q", bob)
	}
	if !strings.Contains(bob, "Bike") || !strings.Contains(bob, "40 more") {
		t.Errorf("Bob's page hides the Reward out of reach: %q", bob)
	}
	carol := section(t, memberPage(t, h, "3"), "member-points")
	if !strings.Contains(carol, `data-balance="3">2<`) || strings.Contains(carol, "Ask for") || !strings.Contains(carol, "1 more") {
		t.Errorf("Carol, on 2 points, can afford something: %q", carol)
	}

	kid := h.Device()
	code, page := kid.Post(t, "/rewards/1/redeem", url.Values{"member_id": {"2"}, "next": {"/members/2"}})
	if code != http.StatusOK || !strings.Contains(page, `data-member-page="2"`) {
		t.Fatalf("redeeming returned %d to %q, want Bob's page", code, page)
	}
	if n := count(t, h, "SELECT count(*) FROM redemptions WHERE member_id = 2 AND reward_id = 1 AND status = 'pending'"); n != 1 {
		t.Fatalf("Bob's pending Redemptions = %d, want 1", n)
	}
	if bob := section(t, page, "member-points"); !strings.Contains(bob, "Ice cream") || !strings.Contains(bob, "waiting") {
		t.Errorf("Bob's request isn't waiting: %q", bob)
	}
	if strings.Contains(section(t, memberPage(t, h, "3"), "member-points"), "waiting") {
		t.Error("Bob's request shows as waiting on Carol's page")
	}
	if code, _ := kid.Post(t, "/rewards/2/redeem", url.Values{"member_id": {"2"}, "next": {"/members/2"}}); code != http.StatusConflict {
		t.Fatalf("asking for a Reward out of reach = %d, want 409", code)
	}
}

// Decided: the Member page is reached by URL and the Member display only.
func TestNoPageLinksToAMemberPage(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Dishes", "daily", "each", "5", "2"))
	for _, path := range []string{"/today", "/calendar", "/todos", "/lists", "/chores", "/settings", "/weather", "/members/2"} {
		_, body := h.Get(t, path)
		for _, link := range []string{`href="/members/1"`, `href="/members/2"`, `href="/members/3"`} {
			if strings.Contains(body, link) {
				t.Errorf("%s links to %s", path, link)
			}
		}
	}
}
