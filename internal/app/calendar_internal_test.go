package app

import (
	"testing"

	"github.com/DeRusto/goCasa/internal/web"
)

func TestLanesPutOverlapsSideBySide(t *testing.T) {
	evs := []web.CalendarEvent{
		{AllDay: true},
		{From: 9 * 60, To: 10 * 60},     // A, with B
		{From: 9*60 + 30, To: 11 * 60},  // B, with A then C
		{From: 10 * 60, To: 10*60 + 30}, // C reuses A's lane
		{From: 11 * 60, To: 12 * 60},    // D starts as B ends: alone
		{From: 13 * 60, To: 13 * 60},    // E zero-length
		{From: 13 * 60, To: 14 * 60},    // F overlaps E's minute
	}
	lanes(evs)
	want := [][2]int{{0, 0}, {0, 2}, {1, 2}, {0, 2}, {0, 1}, {0, 2}, {1, 2}}
	for i, w := range want {
		if got := [2]int{evs[i].Lane, evs[i].Lanes}; got != w {
			t.Errorf("event %d: lane, lanes = %v, want %v", i, got, w)
		}
	}
}
