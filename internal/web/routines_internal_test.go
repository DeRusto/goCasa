package web

import (
	"context"
	"strings"
	"testing"

	"github.com/dop251/goja"
)

// fakeBuilder is the Routine builder pop-up with its lists showing the
// Chores the page was loaded with. fetch hangs until the test answers it with
// respond(ok, html), so a test sees the builder while it is still loading.
const fakeBuilder = `
var window = globalThis, fetched = [], answer;
function fakeEl() {
	return { hidden: false, textContent: "", children: [], listeners: {},
		addEventListener(t, f) { (this.listeners[t] = this.listeners[t] || []).push(f); },
		replaceChildren(...kids) { this.children = kids; },
		querySelector(sel) { return sel.startsWith("form") && this.children.some(k => k.includes('method="post"')) ? {} : null; },
		querySelectorAll() { return []; } };
}
var content = fakeEl(), status = fakeEl(), box = fakeEl();
content.children = ["stale lists"];
box.querySelector = sel => sel === "[data-routine-content]" ? content : sel === "[data-routine-status]" ? status : null;
var document = { getElementById: id => id === "routine-builder" ? box : null };
class AbortController { constructor() { this.signal = { aborted: false }; } abort() { this.signal.aborted = true; } }
function fetch(url, opts) {
	fetched.push(url);
	return new Promise((resolve, reject) => {
		answer = (ok, html) => ok === null ? reject(new Error("offline")) : resolve({ ok, text: async () => html });
	});
}
class DOMParser {
	parseFromString(html) {
		return { querySelector: sel => sel === "#routine-builder [data-routine-content]" && html.includes("data-routine-content") ? { children: [html] } : null };
	}
}
function toggle(newState) { for (const f of box.listeners.beforetoggle || []) f({ newState }); }
function builder() { return JSON.stringify({ shown: !content.hidden, lists: content.children, status: status.hidden ? "" : status.textContent }); }
`

// openBuilder runs the Routine builder's script on fakeBuilder.
func openBuilder(t *testing.T) *goja.Runtime {
	t.Helper()
	var b strings.Builder
	if err := routineBuilderScript().Render(context.Background(), &b); err != nil {
		t.Fatal(err)
	}
	vm := goja.New()
	src := fakeBuilder
	for _, m := range scriptBody.FindAllStringSubmatch(b.String(), -1) {
		src += m[1] + "\n"
	}
	if _, err := vm.RunString(src); err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	return vm
}

func js(t *testing.T, vm *goja.Runtime, code string) string {
	t.Helper()
	v, err := vm.RunString(code)
	if err != nil {
		t.Fatalf("%s: %v", code, err)
	}
	return v.String()
}

// The builder sits outside the live-refreshed board, so a Chore archived or
// changed elsewhere would linger in it; it shows nothing to edit until the
// current lists are in.
func TestTheRoutineBuilderShowsTheCurrentListsEachTimeItOpens(t *testing.T) {
	t.Parallel()
	vm := openBuilder(t)
	js(t, vm, `toggle("open")`)
	if got, want := js(t, vm, "builder()"), `{"shown":false,"lists":["stale lists"],"status":"Loading Routines…"}`; got != want {
		t.Errorf("while loading the builder is %s, want %s", got, want)
	}
	if got := js(t, vm, "JSON.stringify(fetched)"); got != `["/chores"]` {
		t.Errorf("fetched %s, want the chore board", got)
	}
	js(t, vm, `answer(true, '<form method="post" data-routine-content>fresh lists</form>')`)
	if got, want := js(t, vm, "builder()"), `{"shown":true,"lists":["<form method=\"post\" data-routine-content>fresh lists</form>"],"status":""}`; got != want {
		t.Errorf("once loaded the builder is %s, want %s", got, want)
	}
}

func TestTheRoutineBuilderKeepsStaleListsHiddenWhenLoadingFails(t *testing.T) {
	t.Parallel()
	for name, answer := range map[string]string{
		"an error page": `answer(false, "oops")`,
		"no builder":    `answer(true, "<p>signed out</p>")`,
		"offline":       `answer(null)`,
	} {
		vm := openBuilder(t)
		js(t, vm, `toggle("open")`)
		js(t, vm, answer)
		if got, want := js(t, vm, "builder()"), `{"shown":false,"lists":["stale lists"],"status":"Couldn’t load Routines. Close and reopen to try again."}`; got != want {
			t.Errorf("after %s the builder is %s, want %s", name, got, want)
		}
	}
}

// Closing the builder while it loads drops that load: a late answer doesn't
// open lists in a closed pop-up.
func TestClosingTheRoutineBuilderDropsItsLoad(t *testing.T) {
	t.Parallel()
	vm := openBuilder(t)
	js(t, vm, `toggle("open")`)
	js(t, vm, `toggle("closed")`)
	js(t, vm, `answer(true, '<form method="post" data-routine-content>fresh lists</form>')`)
	if got := js(t, vm, "builder()"); !strings.Contains(got, `"shown":false`) || !strings.Contains(got, "stale lists") {
		t.Errorf("a load answered after closing left the builder %s", got)
	}
}
