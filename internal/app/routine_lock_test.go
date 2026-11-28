package app_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
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
