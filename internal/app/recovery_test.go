package app_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/apptest"
)

func TestFiveWrongPINsBlockThatAdminForFiveMinutesOnEveryDevice(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)
	h.Post(t, "/settings/members", member("Bob", "#22c55e", "admin", "5678")) // id 2

	guesser := h.Device()
	for i := 0; i < 4; i++ {
		unlock(t, guesser, 1, "0000")
	}
	if code, _ := unlock(t, guesser, 1, "1234"); code != http.StatusOK {
		t.Fatalf("right PIN after 4 wrong = %d", code)
	}
	for i := 0; i < 5; i++ {
		unlock(t, guesser, 1, "0000")
	}
	if code, body := unlock(t, h.Device(), 1, "1234"); code != http.StatusTooManyRequests || !strings.Contains(body, "blocked for 5 minutes") {
		t.Fatalf("right PIN on another device while blocked = %d", code)
	}
	if code, _ := unlock(t, h.Device(), 2, "5678"); code != http.StatusOK {
		t.Fatalf("other Admin blocked too = %d", code)
	}
	h.Clock.Advance(5 * time.Minute)
	if code, _ := unlock(t, h.Device(), 1, "1234"); code != http.StatusOK {
		t.Fatalf("right PIN after the block = %d", code)
	}
}

func TestAnAdminResetsAnotherAdminsPINAndClearsTheBlock(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)
	h.Post(t, "/settings/members", member("Bob", "#22c55e", "admin", "5678")) // id 2
	for i := 0; i < 5; i++ {
		unlock(t, h.Device(), 2, "0000")
	}
	h.Post(t, "/settings/members/2", member("Bob", "#22c55e", "admin", "2468"))
	if code, _ := unlock(t, h.Device(), 2, "2468"); code != http.StatusOK {
		t.Fatalf("reset PIN = %d", code)
	}
}

func TestArchivedAdminLeavesThePickerAndCantUnlock(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)
	if code, body := h.Post(t, "/settings/members/1/archive", nil); code != http.StatusConflict || !strings.Contains(body, "at least one Admin") {
		t.Fatalf("archive last Admin = %d", code)
	}
	h.Post(t, "/settings/members", member("Bob", "#22c55e", "admin", "5678")) // id 2
	bobs := h.Device()
	unlock(t, bobs, 2, "5678")
	if code, body := h.Post(t, "/settings/members/2/archive", nil); code != http.StatusOK || !strings.Contains(body, "Restore") {
		t.Fatalf("archive Bob = %d", code)
	}
	if _, body := bobs.Get(t, "/settings"); !strings.Contains(body, lockPage) || strings.Contains(body, "Bob") {
		t.Fatalf("archived Admin still unlocked or in the picker: %q", body)
	}
	if code, _ := unlock(t, bobs, 2, "5678"); code != http.StatusUnauthorized {
		t.Fatalf("archived Admin unlock = %d", code)
	}
	// Bob no longer counts: Ada is the last active Admin.
	if code, _ := h.Post(t, "/settings/members/1/archive", nil); code != http.StatusConflict {
		t.Fatalf("archive last active Admin = %d", code)
	}
	if code, _ := h.Post(t, "/settings/members/1", member("Ada", "#3b82f6", "member", "")); code != http.StatusConflict {
		t.Fatalf("demote last active Admin = %d", code)
	}
	if code, _ := h.Post(t, "/settings/members/1/delete", nil); code != http.StatusConflict {
		t.Fatalf("delete last active Admin = %d", code)
	}
	h.Post(t, "/settings/members/2/restore", nil)
	if code, _ := unlock(t, bobs, 2, "5678"); code != http.StatusOK {
		t.Fatalf("restored Admin unlock = %d", code)
	}
}

func TestOnlyAMemberWithNoHistoryCanBeDeleted(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)
	h.Post(t, "/settings/members", member("Rex", "#22c55e", "member", "")) // id 2
	// Stands in for any history table (completions, redemptions, …) that references members.
	if _, err := h.App.DB.Exec("CREATE TABLE history (member_id INTEGER NOT NULL REFERENCES members(id)); INSERT INTO history VALUES (2)"); err != nil {
		t.Fatal(err)
	}
	if code, body := h.Post(t, "/settings/members/2/delete", nil); code != http.StatusConflict || !strings.Contains(body, "Archive") {
		t.Fatalf("delete Member with history = %d", code)
	}
	h.App.DB.Exec("DELETE FROM history")
	if code, body := h.Post(t, "/settings/members/2/delete", nil); code != http.StatusOK || strings.Contains(body, "Rex") {
		t.Fatalf("delete Member without history = %d", code)
	}
}

func TestResetPINFromTheCommandLine(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)
	h.Post(t, "/settings/members", member("Rex", "#22c55e", "member", ""))
	for i := 0; i < 5; i++ {
		unlock(t, h.Device(), 1, "0000")
	}
	pin, err := h.App.ResetPIN(context.Background(), "ada")
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := unlock(t, h.Device(), 1, pin); code != http.StatusOK {
		t.Fatalf("unlock with reset PIN %q = %d", pin, code)
	}
	if code, _ := unlock(t, h.Device(), 1, "1234"); code != http.StatusUnauthorized {
		t.Fatalf("old PIN still works = %d", code)
	}
	for _, who := range []string{"Rex", "Nobody", ""} {
		if _, err := h.App.ResetPIN(context.Background(), who); err == nil {
			t.Fatalf("ResetPIN(%q) succeeded; only Admins have a PIN", who)
		}
	}
}

func TestResetPINPrefersAnExactID(t *testing.T) {
	t.Parallel()
	h := apptest.New(t)
	setup(t, h)
	h.Post(t, "/settings/members/1", member("2", "#3b82f6", "admin", ""))
	h.Post(t, "/settings/members", member("1", "#22c55e", "admin", "5678")) // id 2
	for id, who := range map[int64]string{1: "1", 2: "2"} {
		pin, err := h.App.ResetPIN(context.Background(), who)
		if err != nil {
			t.Fatalf("ResetPIN(%q): %v", who, err)
		}
		if code, _ := unlock(t, h.Device(), id, pin); code != http.StatusOK {
			t.Fatalf("ResetPIN(%q) didn't reset Admin %d", who, id)
		}
	}
}
