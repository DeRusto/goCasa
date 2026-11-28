package app

import (
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
)

func TestCurrentRoutineFollowsTheSlotTimesAtTheirEdges(t *testing.T) {
	t.Parallel()
	kolkata, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	def := Household{Household: web.Household{MorningEnd: 12 * 60, AfternoonEnd: 17 * 60}, Zone: time.UTC}
	custom := Household{Household: web.Household{MorningEnd: 11*60 + 30, AfternoonEnd: 16 * 60}, Zone: kolkata}
	for _, c := range []struct {
		h    Household
		at   time.Time
		want string
	}{
		{def, time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC), "morning"},
		{def, time.Date(2026, 3, 15, 11, 59, 59, 0, time.UTC), "morning"},
		{def, time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC), "afternoon"},
		{def, time.Date(2026, 3, 15, 16, 59, 59, 0, time.UTC), "afternoon"},
		{def, time.Date(2026, 3, 15, 17, 0, 0, 0, time.UTC), "evening"},
		{def, time.Date(2026, 3, 15, 23, 59, 59, 0, time.UTC), "evening"},
		{custom, time.Date(2026, 3, 15, 11, 29, 0, 0, kolkata), "morning"},
		{custom, time.Date(2026, 3, 15, 11, 30, 0, 0, kolkata), "afternoon"},
		{custom, time.Date(2026, 3, 15, 10, 30, 0, 0, time.UTC), "evening"}, // 16:00 in Kolkata
		{custom, time.Date(2026, 3, 15, 10, 29, 0, 0, time.UTC), "afternoon"},
	} {
		if got := c.h.Routine(c.at); got != c.want {
			t.Errorf("Routine at %v = %q, want %q", c.at, got, c.want)
		}
	}
}
