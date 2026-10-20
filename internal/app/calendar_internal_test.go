package app

import (
	"testing"
	"time"

	"github.com/DeRusto/goCasa/internal/web"
)

func TestLanesPutOverlapsSideBySide(t *testing.T) {
	evs := []web.CalendarEvent{
		{AllDay: true},
		{From: 9 * 60, To: 10 * 60},       // A, with B
		{From: 9*60 + 30, To: 11 * 60},    // B, with A then C
		{From: 10 * 60, To: 10*60 + 30},   // C reuses A's lane
		{From: 11 * 60, To: 12 * 60},      // D starts as B ends: alone
		{From: 13 * 60, To: 13 * 60},      // E zero-length
		{From: 13 * 60, To: 14 * 60},      // F overlaps E's minute
		{From: 15 * 60, To: 15*60 + 5},    // G five minutes, drawn taller
		{From: 15*60 + 5, To: 15*60 + 10}, // H would sit under G's block
	}
	lanes(evs)
	want := [][2]int{{0, 0}, {0, 2}, {1, 2}, {0, 2}, {0, 1}, {0, 2}, {1, 2}, {0, 2}, {1, 2}}
	for i, w := range want {
		if got := [2]int{evs[i].Lane, evs[i].Lanes}; got != w {
			t.Errorf("event %d: lane, lanes = %v, want %v", i, got, w)
		}
	}
}

func TestCalendarDayDrawsAnEventInTheRepeatedHourAtItsLength(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, la)          // clocks go back from 02:00 PDT to 01:00 PST
	start := time.Date(2026, 11, 1, 8, 50, 0, 0, time.UTC) // 01:50 PDT
	end := start.Add(20 * time.Minute)                     // 01:10 PST
	d := calendarDay([]calendarEvent{{start: start.Unix(), end: end.Unix()}}, day, day, 0, "15:04")
	if e := d.Events[0]; e.From != 110 || e.To != 130 {
		t.Errorf("From, To = %d, %d; want 110, 130", e.From, e.To)
	}
}
