package app_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/DeRusto/goCasa/internal/apptest"
)

// balance is Member id's points as the chore board shows them.
func balance(t *testing.T, h *apptest.Harness, id string) string {
	t.Helper()
	_, b, ok := strings.Cut(choreBoard(t, h), `data-balance="`+id+`">`)
	if !ok {
		t.Fatalf("no balance for Member %s", id)
	}
	b, _, _ = strings.Cut(b, "<")
	return strings.TrimSpace(b)
}

func TestBalanceIsCompletionsLessApprovedRedemptionsPlusAdjustments(t *testing.T) {
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Dishes", "daily", "each", "5", "2", "3"))
	complete(h, t, "1", "2", "2026-03-15")
	if got := balance(t, h, "2"); got != "5" {
		t.Fatalf("Bob's balance = %s, want 5", got)
	}
	if got := balance(t, h, "3"); got != "0" {
		t.Fatalf("Carol's balance = %s, want 0", got)
	}
	if strings.Contains(choreBoard(t, h), `data-balance="4"`) {
		t.Fatal("a pet has a balance")
	}

	post(t, h, "/rewards", url.Values{"name": {"Ice cream"}, "points": {"3"}})
	kid := h.Device()
	post(t, kid, "/rewards/1/redeem", url.Values{"member_id": {"2"}})
	post(t, kid, "/rewards/1/redeem", url.Values{"member_id": {"2"}})
	if b := choreBoard(t, kid); !strings.Contains(b, "Bob asks for Ice cream") || balance(t, kid, "2") != "5" {
		t.Fatalf("a pending Redemption spent points or isn't on the board: %q", b)
	}
	post(t, h, "/redemptions/1/approve", nil)
	post(t, h, "/redemptions/2/decline", nil)
	if got := balance(t, h, "2"); got != "2" {
		t.Fatalf("after approving one and declining one, Bob's balance = %s, want 2", got)
	}
	if strings.Contains(choreBoard(t, h), "asks for") {
		t.Fatal("decided Redemptions are still pending")
	}
	if n := count(t, h, "SELECT count(*) FROM redemptions WHERE decided_by = 1 AND decided_at IS NOT NULL"); n != 2 {
		t.Fatalf("decisions recorded with their Admin = %d, want 2", n)
	}

	if code, _ := h.Post(t, "/adjustments", url.Values{"member_id": {"2"}, "points": {"-4"}}); code != http.StatusBadRequest {
		t.Fatalf("an Adjustment with no reason = %d, want 400", code)
	}
	post(t, h, "/adjustments", url.Values{"member_id": {"2"}, "points": {"-4"}, "reason": {"Broke a plate"}})
	if got := balance(t, h, "2"); got != "-2" {
		t.Fatalf("after an Adjustment of -4, Bob's balance = %s, want -2", got)
	}
}

func TestARedemptionCannotTakeABalanceBelowZero(t *testing.T) {
	h := choreHousehold(t)
	post(t, h, "/chores", chore("Dishes", "daily", "each", "5", "2"))
	complete(h, t, "1", "2", "2026-03-15")
	post(t, h, "/rewards", url.Values{"name": {"Movie"}, "points": {"4"}})
	post(t, h, "/rewards", url.Values{"name": {"Bike"}, "points": {"50"}})

	if code, _ := h.Post(t, "/rewards/2/redeem", url.Values{"member_id": {"2"}}); code != http.StatusConflict {
		t.Fatalf("asking for more than the balance = %d, want 409", code)
	}
	post(t, h, "/rewards/1/redeem", url.Values{"member_id": {"2"}})
	post(t, h, "/rewards/1/redeem", url.Values{"member_id": {"2"}})
	post(t, h, "/redemptions/1/approve", nil)
	if code, _ := h.Post(t, "/redemptions/2/approve", nil); code != http.StatusConflict {
		t.Fatalf("approving past zero = %d, want 409", code)
	}
	if got := balance(t, h, "2"); got != "1" {
		t.Fatalf("balance = %s, want 1", got)
	}
	if n := count(t, h, "SELECT count(*) FROM redemptions WHERE status = 'pending'"); n != 1 {
		t.Fatalf("pending = %d, want the refused one still pending", n)
	}
	if code, _ := h.Post(t, "/redemptions/1/decline", nil); code != http.StatusNotFound {
		t.Fatalf("deciding a decided Redemption = %d, want 404", code)
	}
}

func TestOnlyAnAdminDecidesAndAdjusts(t *testing.T) {
	h := choreHousehold(t)
	post(t, h, "/rewards", url.Values{"name": {"Sticker"}, "points": {"1"}})
	post(t, h, "/adjustments", url.Values{"member_id": {"2"}, "points": {"3"}, "reason": {"Helped out"}})
	kid := h.Device()
	post(t, kid, "/rewards/1/redeem", url.Values{"member_id": {"2"}})
	for path, f := range map[string]url.Values{
		"/rewards":               {"name": {"Toy"}, "points": {"5"}},
		"/rewards/1/archive":     nil,
		"/redemptions/1/approve": nil,
		"/redemptions/1/decline": nil,
		"/adjustments":           {"member_id": {"2"}, "points": {"9"}, "reason": {"Sneaky"}},
	} {
		if !asksForPIN(t, kid, path, f) {
			t.Errorf("a locked device did %s", path)
		}
	}
	if balance(t, h, "2") != "3" || count(t, h, "SELECT count(*) FROM redemptions WHERE status = 'pending'") != 1 {
		t.Fatal("a locked device changed points")
	}
	if code, _ := h.Post(t, "/rewards/1/redeem", url.Values{"member_id": {"4"}}); code != http.StatusBadRequest {
		t.Fatalf("a pet asking for a Reward = %d, want 400", code)
	}
	post(t, h, "/settings/locks", url.Values{"lock_chores": {"on"}})
	if !asksForPIN(t, kid, "/rewards/1/redeem", url.Values{"member_id": {"2"}}) {
		t.Fatal("the chore Lock doesn't gate asking for a Reward")
	}
}

func TestRewardsArchiveRatherThanDelete(t *testing.T) {
	h := choreHousehold(t)
	post(t, h, "/adjustments", url.Values{"member_id": {"2"}, "points": {"10"}, "reason": {"Birthday"}})
	if code, _ := h.Post(t, "/rewards", url.Values{"name": {"Free"}, "points": {"0"}}); code != http.StatusBadRequest {
		t.Fatalf("a Reward costing nothing = %d, want 400", code)
	}
	post(t, h, "/rewards", url.Values{"name": {"Ice cream"}, "points": {"3"}})
	post(t, h, "/rewards/1/redeem", url.Values{"member_id": {"2"}})
	post(t, h, "/redemptions/1/approve", nil)
	post(t, h, "/rewards/1/archive", nil)

	if b := choreBoard(t, h); strings.Contains(b, "Ice cream") {
		t.Fatalf("an archived Reward is on the board: %q", b)
	}
	if code, _ := h.Post(t, "/rewards/1/redeem", url.Values{"member_id": {"2"}}); code != http.StatusNotFound {
		t.Fatalf("asking for an archived Reward = %d, want 404", code)
	}
	if balance(t, h, "2") != "7" {
		t.Fatal("archiving a Reward changed what was spent on it")
	}
	post(t, h, "/rewards/1/restore", nil)
	if !strings.Contains(choreBoard(t, h), "Ice cream") {
		t.Fatal("a restored Reward isn't on the board")
	}
}
