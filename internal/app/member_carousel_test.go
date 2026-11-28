package app_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestKidCarouselSkipsAndTicksInOrder(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	teeth := chore("Teeth", "daily", "each", "5", "2")
	teeth.Set("emoji", "🪥")
	post(t, h, "/chores", teeth)
	post(t, h, "/chores", chore("Get dressed", "daily", "each", "3", "2"))
	post(t, h, "/chores", chore("Bins", "weekly", "each", "2", "2"))
	post(t, h, "/chores/routines", routines([]string{"1", "2"}, nil, nil))
	kidStyle(t, h, "2", "Bob", "#22c55e")
	path := "/members/2"
	for _, group := range []string{"morning", "afternoon", "evening", "other"} {
		if !strings.Contains(tile(t, kidPage(t, h, "2"), group), `href="/members/2?carousel=`+group+`"`) {
			t.Errorf("%s tile does not open its carousel", group)
		}
	}
	_, page := h.Get(t, path+"?carousel=morning")
	if !inOrder(page, `data-card="current"`, "🪥", "Teeth", "tap when done", "5 points", `data-card="upcoming"`, "Get dressed") {
		t.Error("Morning does not show Teeth in front and Get dressed on the right")
	}
	_, page = h.Get(t, path+"?carousel=morning&later=1.2026-03-15")
	if !inOrder(page, `data-card="current"`, "🌅", "Get dressed", `data-card="upcoming"`, "Teeth") || balance(t, h, "2") != "0" {
		t.Error("skipping Teeth should move it behind Get dressed without awarding points")
	}
	round := url.Values{"round": {"2026-03-15"}}
	_, page = h.Post(t, path+"/routine-chores/2?carousel=morning&later=1.2026-03-15", round)
	if !inOrder(page, `data-card="done"`, "Get dressed", `data-card="current"`, "Teeth") {
		t.Error("ticking Get dressed should show it on the left and bring Teeth forward")
	}
	_, page = h.Post(t, path+"/routine-chores/1?carousel=morning", round)
	if !strings.Contains(page, "Morning done, Bob!") || balance(t, h, "2") != "8" {
		t.Error("last tick should celebrate and award only the Chores' points")
	}
	_, page = h.Get(t, path+"?carousel=morning")
	if strings.Contains(page, "Morning done, Bob!") {
		t.Error("reopening the finished Routine replayed the celebration")
	}
	_, page = h.Get(t, path+"?carousel=other")
	if !inOrder(page, `data-card="current"`, "⭐", "Bins") {
		t.Error("Other chores does not have a carousel with its fallback emoji")
	}
}

func TestKidCarouselCelebratesOnlyWhenThePINFinishesTheRoutine(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Teeth", "daily", "each", "5", "2"))
	post(t, h, "/chores", chore("Get dressed", "daily", "each", "3", "2"))
	post(t, h, "/chores/routines", routines([]string{"1", "2"}, nil, nil))
	kidStyle(t, h, "2", "Bob", "#22c55e")
	post(t, h, "/settings/locks", url.Values{"lock_chores": {"on"}})
	kid := h.Device()
	path := "/members/2"
	round := url.Values{"round": {"2026-03-15"}}
	_, page := kid.Post(t, path+"/routine-chores/1?carousel=morning", round)
	if !inOrder(page, `data-card="done"`, "Teeth", `data-card="current"`, "Get dressed") || strings.Contains(page, "Morning done, Bob!") {
		t.Error("a provisional tick should advance the carousel without celebrating")
	}
	_, pad := kid.Post(t, path+"/routine-chores/2?carousel=morning", round)
	if !strings.Contains(pad, lockPage) || strings.Contains(pad, "Morning done, Bob!") {
		t.Fatal("last provisional tick should open the PIN pad without celebrating")
	}
	confirm := path + "/routines/morning/confirm"
	pin := url.Values{"member_id": {"1"}, "pin": {"0000"}, "ticks": {confirmTicks(pad)}}
	if code, page := kid.Post(t, confirm, pin); code != http.StatusUnauthorized || strings.Contains(page, "Morning done, Bob!") {
		t.Error("wrong PIN should leave the provisional ticks awaiting confirmation")
	}
	_, page = kid.Get(t, path+"?carousel=morning") // Cancel
	if strings.Contains(page, "Morning done, Bob!") || !strings.Contains(page, "Confirm") || balance(t, h, "2") != "0" {
		t.Error("cancelling should leave both ticks provisional, with Confirm available")
	}
	_, pad = kid.Get(t, confirm) // later Confirm from the tile
	pin.Set("ticks", confirmTicks(pad))
	pin.Set("pin", "1234")
	if code, page := kid.Post(t, confirm, pin); code != http.StatusOK || !strings.Contains(page, "Morning done, Bob!") || balance(t, h, "2") != "8" {
		t.Error("later Confirm should celebrate the finished Routine and credit its Chores")
	}
	_, page = kid.Post(t, confirm, pin)
	if strings.Contains(page, "Morning done, Bob!") {
		t.Error("reposting a consumed PIN batch replayed the celebration")
	}
}

func TestKidCarouselPartialConfirmAndUnlockedLastTick(t *testing.T) {
	t.Parallel()
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Teeth", "daily", "each", "5", "2"))
	post(t, h, "/chores", chore("Get dressed", "daily", "each", "3", "2"))
	post(t, h, "/chores", chore("Bins", "weekly", "each", "2", "2"))
	post(t, h, "/chores/routines", routines([]string{"1", "2"}, nil, nil))
	kidStyle(t, h, "2", "Bob", "#22c55e")
	post(t, h, "/settings/locks", url.Values{"lock_chores": {"on"}})
	kid := h.Device()
	path := "/members/2"
	round := url.Values{"round": {"2026-03-15"}}
	kid.Post(t, path+"/routine-chores/1?carousel=morning", round)
	confirm := path + "/routines/morning/confirm?carousel=morning"
	_, pad := kid.Get(t, confirm)
	pin := url.Values{"member_id": {"1"}, "pin": {"1234"}, "ticks": {confirmTicks(pad)}}
	if code, page := kid.Post(t, confirm, pin); code != http.StatusOK || strings.Contains(page, "Morning done, Bob!") || balance(t, h, "2") != "5" {
		t.Error("Confirm before the last tick should credit only the ticked Chore, without a celebration")
	}
	if code, page := kid.Post(t, path+"/routine-chores/3?carousel=other", round); code != http.StatusOK || !strings.Contains(page, lockPage) {
		t.Error("Other chores should still require the regular unlock")
	}
	if code, _ := unlock(t, kid, 1, "1234"); code != http.StatusOK {
		t.Fatalf("unlock returned %d", code)
	}
	if code, page := kid.Post(t, path+"/routine-chores/2?carousel=morning", round); code != http.StatusOK || !strings.Contains(page, "Morning done, Bob!") || balance(t, h, "2") != "8" {
		t.Error("the last tick on an unlocked device should credit the Chore and celebrate without a second PIN")
	}
	if code, page := kid.Post(t, path+"/routine-chores/3?carousel=other", round); code != http.StatusOK || !strings.Contains(page, "All done!") || strings.Contains(page, "Other chores done, Bob!") || balance(t, h, "2") != "10" {
		t.Error("the last tick in Other chores should credit it and show All done")
	}
}
