package app

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A Split moving a Series an hour earlier moves its EXDATEs and RDATEs too,
// with or without parameters on the line.
func TestSplitShiftsBareExdatesAndRdates(t *testing.T) {
	t.Parallel()
	a := household(t)
	master := googleEvent{Start: googleTime{DateTime: "2026-03-14T10:00:00Z", TimeZone: "Europe/London"}}
	o := time.Date(2026, 3, 21, 10, 0, 0, 0, time.UTC).Unix()
	c := eventChange{start: time.Date(2026, 3, 21, 9, 0, 0, 0, time.UTC).Unix(), tzid: "Europe/London"}
	got := a.shiftDates(context.Background(), []string{"RRULE:FREQ=WEEKLY", "EXDATE:20260404T090000Z", "RDATE:20260401T100000"}, master, o, c)
	if want := "[RRULE:FREQ=WEEKLY EXDATE;TZID=Europe/London:20260404T090000 RDATE;TZID=Europe/London:20260401T090000]"; fmt.Sprint(got) != want {
		t.Fatalf("shifted %v, want %s", got, want)
	}
}
