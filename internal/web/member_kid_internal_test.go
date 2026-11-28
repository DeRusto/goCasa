package web

import (
	"context"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// fakeLive is a Member page's #live for keepPanelOpen: htmx's swap events and
// pop-ups, open or not, by id; a swap replaces every pop-up with a closed one.
const fakeLive = `
var handlers = {}, panels = {};
function panel(id, open) { panels[id] = { id, open, showPopover() { this.open = true; } }; }
var live = {
	addEventListener(type, f) { handlers[type] = f; },
	querySelector(sel) { return Object.values(panels).find(p => p.open) || null; },
};
var document = { getElementById: id => id === "live" ? live : panels[id] || null };
function swap() {
	handlers["htmx:beforeSwap"]();
	for (const id in panels) panel(id, false);
	handlers["htmx:afterSwap"]();
}
`

func runKeepPanelOpen(t *testing.T, js string) goja.Value {
	t.Helper()
	var b strings.Builder
	if err := keepPanelOpen().Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	vm := goja.New()
	src := fakeLive
	for _, m := range scriptBody.FindAllStringSubmatch(b.String(), -1) {
		src += m[1] + "\n"
	}
	v, err := vm.RunString(src + js)
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	return v
}

// A live refresh while a child has the Rewards open keeps them open.
func TestKeepPanelOpenReopensTheOpenPanelAfterALiveSwap(t *testing.T) {
	v := runKeepPanelOpen(t, `panel("kid-rewards", true); panel("kid-weather", false); swap();
		[panels["kid-rewards"].open, panels["kid-weather"].open].join()`)
	if got := v.String(); got != "true,false" {
		t.Errorf("after a swap the Rewards, weather are open = %s, want true,false", got)
	}
	v = runKeepPanelOpen(t, `panel("kid-rewards", false); swap(); panels["kid-rewards"].open`)
	if v.ToBoolean() {
		t.Error("a swap opened a pop-up nobody had open")
	}
}
