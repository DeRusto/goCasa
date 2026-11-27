package web_test

import (
	"strconv"
	"testing"

	"github.com/DeRusto/goCasa/internal/web"
	"github.com/dop251/goja"
)

// A page script test runs static/bridge.js in goja, a JavaScript engine in Go,
// so it runs under go test with no Node or browser. The page around it is a
// small fake document: the script tag's data attributes, a #wake link that
// counts its clicks, and a body that keeps what's appended to it.
const fakePage = `
var window = globalThis;
var listeners = {}, clicks = 0, appended = [];
var document = {
	readyState: "loading",
	currentScript: { dataset: { screen: SCREEN, cameraWake: CAMERA } },
	getElementById: id => id === "wake" && HAS_WAKE ? { click() { clicks++; } } : null,
	addEventListener(type, f) { (listeners[type] = listeners[type] || []).push(f); },
	createElement: tag => ({ tagName: tag, style: {}, textContent: "", setAttribute(k, v) { this[k] = v; } }),
	body: { append(...els) { appended.push(...els); } },
};
function getComputedPointerEvents(el) { return el && el.style.pointerEvents; }
function loaded() { document.readyState = "interactive"; for (const f of listeners.DOMContentLoaded || []) f(); }
`

// fakeApp is a Display app's window.gocasaDisplay that keeps what it is sent.
const fakeApp = `
var sent = [];
window.gocasaDisplay = { build: "apk", version: VERSION, send(m) { sent.push(JSON.parse(JSON.stringify(m))); } };
`

type pageOpts struct {
	screen     string
	cameraWake bool
	hasWake    bool
	app        bool   // a Display app's window.gocasaDisplay exists
	version    string // its version
}

// loadPage runs bridge.js on a fake page as the browser would, then fires DOMContentLoaded.
func loadPage(t *testing.T, o pageOpts) *goja.Runtime {
	t.Helper()
	src, err := web.Static.ReadFile("static/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	vm := goja.New()
	for k, v := range map[string]any{"SCREEN": o.screen, "CAMERA": strconv.FormatBool(o.cameraWake), "HAS_WAKE": o.hasWake, "VERSION": o.version} {
		if err := vm.Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	run(t, vm, fakePage)
	if o.app {
		run(t, vm, fakeApp)
	}
	run(t, vm, string(src))
	run(t, vm, "loaded()")
	return vm
}

func run(t *testing.T, vm *goja.Runtime, js string) goja.Value {
	t.Helper()
	v, err := vm.RunString(js)
	if err != nil {
		t.Fatalf("%s: %v", js, err)
	}
	return v
}

// asJSON is a JavaScript expression's value as JSON, for comparing with a literal.
func asJSON(t *testing.T, vm *goja.Runtime, expr string) string {
	t.Helper()
	return run(t, vm, "JSON.stringify("+expr+")").String()
}

func TestTheBridgeSendsThePageStateToTheDisplayAppAfterLoad(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		screen     string
		cameraWake bool
		want       string
	}{
		{"awake", false, `[{"type":"state","screen":"awake","cameraWake":false}]`},
		{"screensaver", true, `[{"type":"state","screen":"screensaver","cameraWake":true}]`},
		{"night-black", false, `[{"type":"state","screen":"night-black","cameraWake":false}]`},
		{"night-dim", true, `[{"type":"state","screen":"night-dim","cameraWake":true}]`},
	} {
		vm := loadPage(t, pageOpts{screen: c.screen, cameraWake: c.cameraWake, app: true, version: "1.0.0"})
		if got := asJSON(t, vm, "sent"); got != c.want {
			t.Errorf("%s page sent %s, want %s", c.screen, got, c.want)
		}
	}
}

func TestAWakeFromTheDisplayAppTouchesWake(t *testing.T) {
	t.Parallel()
	for _, msg := range []string{
		`{type: "wake"}`,
		`{data: {type: "wake"}}`,    // a MessageEvent
		`{data: '{"type":"wake"}'}`, // a MessageEvent carrying JSON text, as Android's postMessage does
	} {
		vm := loadPage(t, pageOpts{screen: "screensaver", hasWake: true, app: true, version: "1.0.0"})
		run(t, vm, "gocasaDisplay.onmessage("+msg+")")
		if got := asJSON(t, vm, "clicks"); got != "1" {
			t.Errorf("after %s #wake was clicked %s times, want 1", msg, got)
		}
	}
}

func TestTheBridgeIgnoresMessagesItDoesntKnow(t *testing.T) {
	t.Parallel()
	vm := loadPage(t, pageOpts{screen: "screensaver", hasWake: true, app: true, version: "1.0.0"})
	for _, msg := range []string{`{type: "dance"}`, `{}`, `null`, `"wake"`, `{data: "not json"}`} {
		run(t, vm, "gocasaDisplay.onmessage("+msg+")")
	}
	if got := asJSON(t, vm, "clicks"); got != "0" {
		t.Errorf("unknown messages clicked #wake %s times", got)
	}
}

func TestAWakeOnAPageWithoutWakeDoesNothing(t *testing.T) {
	t.Parallel()
	vm := loadPage(t, pageOpts{screen: "awake", app: true, version: "1.0.0"})
	run(t, vm, `gocasaDisplay.onmessage({type: "wake"})`)
}

// noticeText is what the bridge appended to the page, as text.
const noticeText = `appended.map(e => e.textContent).join("|")`

func TestAnOldDisplayAppGetsAnUpdateNoticeAndNoBridge(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"0.0.9", "", "banana", "0.0"} { // older than GoCasa's minimum, 0.1.0, or no version at all
		vm := loadPage(t, pageOpts{screen: "screensaver", hasWake: true, app: true, version: v})
		if got := run(t, vm, noticeText).String(); got != "Update the Display app" {
			t.Errorf("version %q: page shows %q, want the update notice", v, got)
		}
		if got := run(t, vm, "getComputedPointerEvents(appended[0])").String(); got != "none" {
			t.Errorf("version %q: the notice catches touches (pointer-events %q)", v, got)
		}
		if got := asJSON(t, vm, "sent"); got != "[]" {
			t.Errorf("version %q: sent %s to an app it can't rely on", v, got)
		}
		if got := asJSON(t, vm, "!!window.gocasaBridge"); got != "false" {
			t.Errorf("version %q: the page's own wake lock and camera were told to stand down", v)
		}
		if run(t, vm, `typeof gocasaDisplay.onmessage`).String() == "function" {
			t.Errorf("version %q: the bridge listens for wakes", v)
		}
	}
}

func TestASupportedDisplayAppGetsTheBridgeAndNoNotice(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"0.1.0", "0.1.1", "0.10.0", "1.0.0", "1.0.0-rc.2", "12.3.4"} {
		vm := loadPage(t, pageOpts{screen: "awake", app: true, version: v})
		if got := run(t, vm, noticeText).String(); got != "" {
			t.Errorf("version %q: page shows %q", v, got)
		}
		if got := asJSON(t, vm, "window.gocasaBridge"); got != "true" {
			t.Errorf("version %q: gocasaBridge = %s, want true", v, got)
		}
		if got := asJSON(t, vm, "sent.length"); got != "1" {
			t.Errorf("version %q: sent %s messages, want the state", v, got)
		}
	}
}

func TestWithoutTheDisplayAppTheBridgeDoesNothing(t *testing.T) {
	t.Parallel()
	vm := loadPage(t, pageOpts{screen: "screensaver", hasWake: true})
	if got := asJSON(t, vm, "[!!window.gocasaBridge, appended.length, clicks, Object.keys(listeners)]"); got != "[false,0,0,[]]" {
		t.Errorf("with no Display app the bridge left %s", got)
	}
}
