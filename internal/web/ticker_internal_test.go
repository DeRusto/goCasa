package web

import (
	"context"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// fakeTicking is a page for ticker: a #clock and, on a Member page, a
// [data-routine-in] element, with the time and setInterval in the test's hands.
const fakeTicking = `
var window = globalThis, NOW = 1e12, tick, refreshes = 0;
Date.now = () => NOW;
function setInterval(f) { tick = f; }
function el(dataset) { return { dataset, textContent: "" }; }
var clock = el({ tz: "UTC", offset: "+00:00", h24: "true", midnightIn: "54000" }), turn = null;
var document = {
	getElementById: id => id === "clock" ? clock : null,
	querySelector: sel => sel === "[data-routine-in]" ? turn : null,
	querySelectorAll: () => [],
	dispatchEvent(e) { if (e.type === "refresh-live") refreshes++; },
};
class Event { constructor(type) { this.type = type; } }
function after(s) { NOW += s * 1000; tick(); return refreshes; }
`

func startTicker(t *testing.T) *goja.Runtime {
	t.Helper()
	var b strings.Builder
	if err := ticker().Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	vm := goja.New()
	src := fakeTicking
	for _, m := range scriptBody.FindAllStringSubmatch(b.String(), -1) {
		src += m[1] + "\n"
	}
	if _, err := vm.RunString(src); err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	return vm
}

func refreshesAfter(t *testing.T, vm *goja.Runtime, js string) int64 {
	t.Helper()
	v, err := vm.RunString(js)
	if err != nil {
		t.Fatal(err)
	}
	return v.ToInteger()
}

// On a Member page the current Routine moves on by itself: once
// data-routine-in seconds are up the page refetches, once, and a refetched
// element starts its own count.
func TestTickerRefreshesWhenTheNextRoutineStarts(t *testing.T) {
	vm := startTicker(t)
	refreshesAfter(t, vm, `turn = el({ routineIn: "60" }); after(0)`)
	if n := refreshesAfter(t, vm, "after(59)"); n != 0 {
		t.Fatalf("refreshed %d times before the Routine turned", n)
	}
	if n := refreshesAfter(t, vm, "after(1)"); n != 1 {
		t.Fatalf("refreshes when the Routine turned = %d, want 1", n)
	}
	if n := refreshesAfter(t, vm, "after(1); after(60)"); n != 1 {
		t.Fatalf("refreshed again on the same element: %d", n)
	}
	if n := refreshesAfter(t, vm, `turn = el({ routineIn: "30" }); after(0); after(30)`); n != 2 {
		t.Fatalf("a refetched element didn't start its own count: %d refreshes", n)
	}
}

// Without a Routine to follow (Today), only midnight refreshes.
func TestTickerWithoutARoutineRefreshesAtMidnightOnly(t *testing.T) {
	vm := startTicker(t)
	if n := refreshesAfter(t, vm, "after(0); after(53999)"); n != 0 {
		t.Fatalf("refreshed %d times before midnight", n)
	}
	if n := refreshesAfter(t, vm, "after(1)"); n != 1 {
		t.Fatalf("refreshes at midnight = %d, want 1", n)
	}
}

// Kid style's clock is plain text: in 12-hour it drops am/pm ("1:46").
func TestTickerDropsAmPmWhenTheClockSaysSo(t *testing.T) {
	vm := startTicker(t)
	v, err := vm.RunString(`Date.prototype.toLocaleTimeString = () => "1:46 AM";
		clock.dataset.h24 = "false"; clock.dataset.ampm = "false"; after(0); clock.textContent`)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.String(); got != "1:46" {
		t.Errorf("a plain 12-hour clock reads %q, want 1:46", got)
	}
	v, _ = vm.RunString(`clock.dataset.ampm = "true"; after(1); clock.textContent`)
	if got := v.String(); got != "1:46 AM" {
		t.Errorf("Today's 12-hour clock reads %q, want 1:46 AM", got)
	}
}
