package app_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestLockedRoutineTickIsProvisionalOnTheMemberPageOnly(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Teeth", "daily", "each", "5", "2", "3"))
	post(t, h, "/chores", chore("Get dressed", "daily", "each", "3", "2"))
	post(t, h, "/chores/routines", routines([]string{"1", "2"}, nil, nil))
	post(t, h, "/settings/locks", url.Values{"lock_chores": {"on"}})
	kid := h.Device()
	path := "/members/2"
	code, page := kid.Post(t, path+"/routine-chores/1", url.Values{"round": {"2026-03-15"}})
	if code != http.StatusOK || !strings.Contains(page, `data-member-page="2"`) {
		t.Fatalf("first tick returned %d, want Bob's Member page", code)
	}
	_, g := groups(t, page)
	for _, want := range []string{"1 of 2", "Bob pending PIN", "Confirm"} {
		if !strings.Contains(g["morning"], want) {
			t.Errorf("Morning lacks %q: %s", want, g["morning"])
		}
	}
	if got := balance(t, h, "2"); got != "0" {
		t.Errorf("balance after provisional tick = %s, want 0", got)
	}
	_, today := kid.Get(t, "/today")
	for _, page := range []string{today, choreBoard(t, kid)} {
		if !strings.Contains(page, "Complete Teeth for Bob") || strings.Contains(page, "Bob pending PIN") {
			t.Error("provisional tick leaked outside the Member page")
		}
	}
}

func TestRoutinePINConfirmsOnlyThisMembersTicks(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Teeth", "daily", "each", "5", "2", "3"))
	post(t, h, "/chores", chore("Get dressed", "daily", "each", "3", "2"))
	post(t, h, "/chores/routines", routines([]string{"1", "2"}, nil, nil))
	post(t, h, "/settings/locks", url.Values{"lock_chores": {"on"}})
	kid := h.Device()
	round := url.Values{"round": {"2026-03-15"}}
	kid.Post(t, "/members/2/routine-chores/1", round)
	kid.Post(t, "/members/3/routine-chores/1", round)
	path := "/members/2/routines/morning/confirm"
	code, page := kid.Post(t, "/members/2/routine-chores/2", round)
	if code != http.StatusOK || !strings.Contains(page, lockPage) || !strings.Contains(page, `href="/members/2">Cancel`) {
		t.Fatalf("final tick returned %d, want a PIN pad with Cancel", code)
	}
	_, morning := groups(t, memberPage(t, kid, "2")) // Cancel goes back to the Member page
	if !strings.Contains(morning["morning"], "2 of 2") || !strings.Contains(morning["morning"], "Confirm") {
		t.Fatal("cancelling lost the provisional ticks")
	}
	pin := url.Values{"member_id": {"1"}, "pin": {"0000"}}
	if code, _ := kid.Post(t, path, pin); code != http.StatusUnauthorized {
		t.Errorf("wrong PIN returned %d, want 401", code)
	}
	if got := balance(t, h, "2"); got != "0" {
		t.Fatalf("wrong PIN earned %s points", got)
	}
	pin.Set("pin", "1234")
	code, page = kid.Post(t, path, pin)
	if code != http.StatusOK {
		t.Fatalf("confirm returned %d", code)
	}
	_, morning = groups(t, page)
	if strings.Contains(morning["morning"], "Confirm") || strings.Count(morning["morning"], "Bob done") != 2 {
		t.Fatal("one PIN did not confirm both ticks")
	}
	kid.Post(t, path, pin) // resubmitting cannot award points twice
	if got := balance(t, h, "2"); got != "8" {
		t.Errorf("Bob's balance = %s, want 8", got)
	}
	_, carol := groups(t, memberPage(t, kid, "3"))
	if !strings.Contains(carol["morning"], "Carol pending PIN") || balance(t, h, "3") != "0" {
		t.Error("Bob's PIN affected Carol's tick")
	}
}

func TestConfirmCreditsTheOriginalLateRoundButSkipsMissedRounds(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Teeth", "daily", "each", "5", "2"))
	post(t, h, "/chores/routines", routines([]string{"1"}, nil, nil))
	post(t, h, "/settings/locks", url.Values{"lock_chores": {"on"}})
	kid := h.Device()
	tick := "/members/2/routine-chores/1"
	confirm := "/members/2/routines/morning/confirm"
	pin := url.Values{"member_id": {"1"}, "pin": {"1234"}}

	kid.Post(t, tick, url.Values{"round": {"2026-03-15"}})
	h.Clock.Advance(24 * time.Hour)
	_, g := groups(t, memberPage(t, kid, "2"))
	if !inOrder(g["morning"], `data-round="2026-03-15"`, "Late", "Bob pending PIN", `data-round="2026-03-16"`) ||
		!strings.Contains(g["morning"], "Confirm") {
		t.Fatalf("reloading after midnight lost the Late tick: %s", g["morning"])
	}
	kid.Post(t, confirm, pin)
	if got := balance(t, h, "2"); got != "5" {
		t.Errorf("balance after confirming Late = %s, want 5", got)
	}
	_, g = groups(t, memberPage(t, kid, "2"))
	if strings.Contains(g["morning"], "2026-03-15") || !strings.Contains(g["morning"], "0 of 1") {
		t.Errorf("confirmation should consume only the Late Round: %s", g["morning"])
	}

	kid.Post(t, tick, url.Values{"round": {"2026-03-16"}})
	h.Clock.Advance(48 * time.Hour)
	kid.Post(t, tick, url.Values{"round": {"2026-03-18"}})
	kid.Post(t, confirm, pin)
	if got := balance(t, h, "2"); got != "10" {
		t.Errorf("confirmation should credit today's Round and skip the Missed one, balance = %s", got)
	}
}

func TestChoresBoardCanTakeOverAProvisionalRoutineTick(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Teeth", "daily", "each", "5", "2"))
	post(t, h, "/chores", chore("Get dressed", "daily", "each", "3", "2"))
	post(t, h, "/chores/routines", routines([]string{"1", "2"}, nil, nil))
	post(t, h, "/settings/locks", url.Values{"lock_chores": {"on"}})
	kid := h.Device()
	round := "2026-03-15"
	kid.Post(t, "/members/2/routine-chores/1", url.Values{"round": {round}})
	kid.Post(t, "/members/2/routine-chores/2", url.Values{"round": {round}})
	f := url.Values{"member_id": {"2"}, "round": {round}}
	if page := postFrom(t, kid, "/chores", "/chores/1/complete", f); !strings.Contains(page, lockPage) {
		t.Fatal("Chores board didn't ask for the PIN")
	}
	unlock(t, kid, 1, "1234")
	complete(kid, t, "1", "2", round)
	if got := balance(t, h, "2"); got != "5" {
		t.Errorf("balance after the board completed a provisional tick = %s, want 5", got)
	}
	pin := url.Values{"member_id": {"1"}, "pin": {"1234"}}
	kid.Post(t, "/members/2/routines/morning/confirm", pin)
	if got := balance(t, h, "2"); got != "8" {
		t.Errorf("balance after confirming the remaining tick = %s, want 8", got)
	}
}

func TestRoutineTicksCountImmediatelyWhenAllowed(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Teeth", "daily", "each", "5", "2", "3"))
	post(t, h, "/chores/routines", routines([]string{"1"}, nil, nil))
	f := url.Values{"round": {"2026-03-15"}, "member_id": {"3"}}
	code, page := h.Device().Post(t, "/members/2/routine-chores/1", f)
	if code != http.StatusOK || !strings.Contains(page, "Bob done") || balance(t, h, "2") != "5" || balance(t, h, "3") != "0" {
		t.Fatal("with the Lock off, the tick should immediately credit only the Member in the URL")
	}
	post(t, h, "/settings/locks", url.Values{"lock_chores": {"on"}})
	code, page = h.Post(t, "/members/3/routine-chores/1", f)
	if code != http.StatusOK || !strings.Contains(page, "Carol done") || balance(t, h, "3") != "5" {
		t.Fatal("on an unlocked device, the tick should immediately credit the Member")
	}
}

func TestRoutinePINUsesTheSameBlockAsTheUnlockPad(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Teeth", "daily", "each", "5", "2"))
	post(t, h, "/chores/routines", routines([]string{"1"}, nil, nil))
	post(t, h, "/settings/locks", url.Values{"lock_chores": {"on"}})
	kid := h.Device()
	kid.Post(t, "/members/2/routine-chores/1", url.Values{"round": {"2026-03-15"}})
	pin := url.Values{"member_id": {"1"}, "pin": {"0000"}}
	for range 4 {
		kid.Post(t, "/lock", pin)
	}
	path := "/members/2/routines/morning/confirm"
	if code, _ := kid.Post(t, path, pin); code != http.StatusTooManyRequests {
		t.Errorf("fifth shared wrong PIN returned %d, want 429", code)
	}
	pin.Set("pin", "1234")
	if code, _ := kid.Post(t, path, pin); code != http.StatusTooManyRequests {
		t.Errorf("blocked PIN returned %d, want 429", code)
	}
	if got := balance(t, h, "2"); got != "0" {
		t.Errorf("blocked PIN earned %s points", got)
	}
}
