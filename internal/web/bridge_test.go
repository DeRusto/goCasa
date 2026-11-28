package web_test

import (
	"regexp"
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
var listeners = {}, windowListeners = {}, clicks = 0, appended = [];
function addEventListener(type, f) { (windowListeners[type] = windowListeners[type] || []).push(f); }
var nav = { hidden: false }, bedtime = { hidden: true, click() { this.onclick?.(); } };
var document = {
	readyState: "loading",
	currentScript: { dataset: { screen: SCREEN, cameraWake: CAMERA } },
	getElementById: id => {
		if (id === "wake" && HAS_WAKE) return { click() { clicks++; } };
		if (id === "main-navigation") return nav;
		if (id === "member-bedtime" && MEMBER) return bedtime;
		return null;
	},
	addEventListener(type, f) { (listeners[type] = listeners[type] || []).push(f); },
	createElement: tag => ({ tagName: tag, style: {}, textContent: "", setAttribute(k, v) { this[k] = v; } }),
	body: { append(...els) { appended.push(...els); } },
};
function getComputedPointerEvents(el) { return el && el.style.pointerEvents; }
function loaded() { document.readyState = "interactive"; for (const f of listeners.DOMContentLoaded || []) f(); }
function finished() { document.readyState = "complete"; for (const f of windowListeners.load || []) f(); }
`

// fakeApp is a Display app's window.gocasaDisplay that keeps what it is sent.
const fakeApp = `
var sent = [];
window.gocasaDisplay = { build: BUILD, version: VERSION, send(m) { sent.push(JSON.parse(JSON.stringify(m))); } };
`

type pageOpts struct {
	member        bool
	build         string
	memberMinimum string
	screen        string
	cameraWake    bool
	hasWake       bool
	app           bool   // a Display app's window.gocasaDisplay exists
	version       string // its version
}

// loadPage runs bridge.js on a fake page as the browser would, then fires
// DOMContentLoaded and, once the page's pictures are in, load.
func loadPage(t *testing.T, o pageOpts) *goja.Runtime {
	t.Helper()
	vm := startPage(t, o)
	run(t, vm, "loaded()")
	run(t, vm, "finished()")
	return vm
}

var memberMinimumLine = regexp.MustCompile(`(?m)^[\t ]*const minimumMemberDisplayVersion = [^;]+;`)

// startPage runs bridge.js on a fake page that hasn't loaded yet.
func startPage(t *testing.T, o pageOpts) *goja.Runtime {
	t.Helper()
	src, err := web.Static.ReadFile("static/bridge.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(src)
	if o.memberMinimum != "" {
		// Exercise both sides of the release gate regardless of the shipped minimum.
		loc := memberMinimumLine.FindStringIndex(script)
		if loc == nil {
			t.Fatal("could not find the Member display release minimum")
		}
		script = script[:loc[0]] + "\tconst minimumMemberDisplayVersion = " + o.memberMinimum + ";" + script[loc[1]:]
	}
	if o.build == "" {
		o.build = "apk"
	}
	vm := goja.New()
	for k, v := range map[string]any{"SCREEN": o.screen, "CAMERA": strconv.FormatBool(o.cameraWake), "HAS_WAKE": o.hasWake, "VERSION": o.version, "MEMBER": o.member, "BUILD": o.build} {
		if err := vm.Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	run(t, vm, fakePage)
	if o.app {
		run(t, vm, fakeApp)
	}
	run(t, vm, script)
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

// The app may turn the screen off on night-black, so the state waits until
// the page has finished loading.
func TestTheBridgeSendsTheStateOnlyOnceThePageHasLoaded(t *testing.T) {
	t.Parallel()
	vm := startPage(t, pageOpts{screen: "night-black", hasWake: true, app: true, version: "1.0.0"})
	run(t, vm, "loaded()")
	if got := asJSON(t, vm, "sent"); got != "[]" {
		t.Errorf("sent %s before the page finished loading", got)
	}
	run(t, vm, "finished()")
	if got := asJSON(t, vm, "sent.length"); got != "1" {
		t.Errorf("sent %s messages after load, want the state", got)
	}
}

func TestAWakeFromTheDisplayAppTouchesWake(t *testing.T) {
	t.Parallel()
	for _, msg := range []string{
		`{type: "wake"}`,
		`{data: {type: "wake"}}`,                     // a MessageEvent
		`{data: '{"type":"wake"}'}`,                  // a MessageEvent carrying JSON text, as Android's postMessage does
		`{type: "message", data: {type: "wake"}}`,    // a real MessageEvent has a type of its own
		`{type: "message", data: '{"type":"wake"}'}`, // and Android's carries JSON text
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
	if got := asJSON(t, vm, "[clicks, sent.length, appended.length]"); got != "[0,1,0]" {
		t.Errorf("a wake with no #wake left [clicks, messages sent, notices] = %s, want only the load state", got)
	}
}

// noticeText is what the bridge appended to the page, as text.
const noticeText = `appended.map(e => e.textContent).join("|")`

func TestAnOldDisplayAppGetsAnUpdateNoticeAndNoBridge(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"0.0.9", "", "banana", "0.0", "0.1.0garbage", "1.0.0-beta", "0.1.0-rc.1"} { // older than GoCasa's minimum, 0.1.0, or no version at all
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
		if run(t, vm, `typeof gocasaDisplay.onmessage`).String() == "function" {
			t.Errorf("version %q: the bridge listens for wakes", v)
		}
	}
}

func TestASupportedDisplayAppGetsTheBridgeAndNoNotice(t *testing.T) {
	t.Parallel()
	for _, v := range []string{"0.1.0", "0.1.1", "0.1.1-rc.1", "0.10.0", "1.0.0", "1.0.0-rc.2", "12.3.4"} {
		vm := loadPage(t, pageOpts{screen: "awake", app: true, version: v})
		if got := run(t, vm, noticeText).String(); got != "" {
			t.Errorf("version %q: page shows %q", v, got)
		}
		if got := asJSON(t, vm, "sent.length"); got != "1" {
			t.Errorf("version %q: sent %s messages, want the state", v, got)
		}
		if run(t, vm, "nav.hidden").ToBoolean() {
			t.Errorf("version %q: main navigation was hidden outside a Member page", v)
		}
	}
}

func TestWithoutTheDisplayAppTheBridgeDoesNothing(t *testing.T) {
	t.Parallel()
	vm := loadPage(t, pageOpts{screen: "screensaver", hasWake: true})
	if got := asJSON(t, vm, "[appended.length, clicks, Object.keys(listeners), Object.keys(windowListeners)]"); got != "[0,0,[],[]]" {
		t.Errorf("with no Display app the bridge left %s", got)
	}
}

func TestMemberPageBridge(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name, build, version           string
		released, hideNav, showBedtime bool
		notice                         string
	}{
		{name: "browser"},
		{name: "unsupported APK", build: "apk", version: "0.0.9", released: true, notice: "Update the Display app"},
		{name: "Wall-only APK", build: "apk", version: "1.1.9", released: true, hideNav: true},
		{name: "extension", build: "extension", version: "1.2.0", released: true, hideNav: true},
		{name: "before release", build: "apk", version: "1.2.0", hideNav: true},
		{name: "release candidate", build: "apk", version: "1.2.0-rc.1", released: true, hideNav: true},
		{name: "Member display APK", build: "apk", version: "1.2.0", released: true, hideNav: true, showBedtime: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			minimum := "null"
			if c.released {
				minimum = "[1, 2, 0]"
			}
			vm := loadPage(t, pageOpts{screen: "awake", member: true, app: c.build != "", build: c.build, version: c.version, memberMinimum: minimum})
			if got := run(t, vm, "nav.hidden").ToBoolean(); got != c.hideNav {
				t.Errorf("main navigation hidden = %t, want %t", got, c.hideNav)
			}
			if got := run(t, vm, "!bedtime.hidden").ToBoolean(); got != c.showBedtime {
				t.Errorf("Bedtime shown = %t, want %t", got, c.showBedtime)
			}
			if got := run(t, vm, noticeText).String(); got != c.notice {
				t.Errorf("notice = %q, want %q", got, c.notice)
			}
			if c.showBedtime {
				run(t, vm, "sent.length = 0; bedtime.click()")
				if got, want := asJSON(t, vm, "sent"), `[{"type":"startBedtime"}]`; got != want {
					t.Errorf("tapping Bedtime sent %s, want %s", got, want)
				}
			}
		})
	}
}
