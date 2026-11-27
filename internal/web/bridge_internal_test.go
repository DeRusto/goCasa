package web

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/dop251/goja"
)

// fakeBrowser is a secure page whose camera and wake lock calls are counted,
// with timers and input listeners kept rather than run.
const fakeBrowser = `
var window = globalThis, isSecureContext = true;
var calls = { camera: 0, wakeLock: 0 }, timers = 0, inputs = [], statuses = {};
var navigator = {
	mediaDevices: { getUserMedia() { calls.camera++; return new Promise(() => {}); } },
	wakeLock: { request() { calls.wakeLock++; return new Promise(() => {}); } },
};
function setTimeout() { timers++; }
function clearTimeout() {}
function addEventListener(type) { inputs.push(type); }
var document = {
	visibilityState: "visible",
	addEventListener() {},
	getElementById: id => ({ set textContent(s) { statuses[id] = s; }, hidden: true }),
};
`

var scriptBody = regexp.MustCompile(`(?s)<script[^>]*>(.*?)</script>`)

// runScripts renders c and runs its scripts in a fake browser, with the
// Bridge active or not, and gives back what they did.
func runScripts(t *testing.T, c templ.Component, bridged bool) string {
	return runScriptsWith(t, c, bridged, false)
}

// runScriptsWith is runScripts with, if old, a Display app too old for the Bridge.
func runScriptsWith(t *testing.T, c templ.Component, bridged, old bool) string {
	t.Helper()
	var b strings.Builder
	if err := c.Render(WithDevice(context.Background(), Device{WallDisplay: true, MotionWake: true}), &b); err != nil {
		t.Fatal(err)
	}
	vm := goja.New()
	if err := vm.Set("BRIDGED", bridged); err != nil {
		t.Fatal(err)
	}
	if err := vm.Set("OLD", old); err != nil {
		t.Fatal(err)
	}
	src := fakeBrowser + "if (BRIDGED) window.gocasaDisplay = { version: OLD ? '0.0.1' : '1.0.0', send() {} };\n"
	for _, m := range scriptBody.FindAllStringSubmatch(b.String(), -1) {
		// document.currentScript is the tag being run; give each its data attributes.
		src += "document.currentScript = { dataset: { idleSeconds: \"300\" } };\n" + m[1] + "\n"
	}
	if _, err := vm.RunString(src); err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	v, err := vm.RunString(`JSON.stringify({ calls, idle: timers > 0 && inputs.includes("pointerdown"), statuses })`)
	if err != nil {
		t.Fatal(err)
	}
	return v.String()
}

func TestWithTheBridgeTheWallDisplayLeavesTheWakeLockToTheAppButStillGoesIdle(t *testing.T) {
	t.Parallel()
	if got, want := runScripts(t, wallDisplay(300), false), `{"calls":{"camera":0,"wakeLock":1},"idle":true,"statuses":{}}`; got != want {
		t.Errorf("in a browser the Wall display did %s, want %s", got, want)
	}
	if got, want := runScripts(t, wallDisplay(300), true), `{"calls":{"camera":0,"wakeLock":0},"idle":true,"statuses":{"wake-lock":"kept on by the Display app"}}`; got != want {
		t.Errorf("with the Bridge the Wall display did %s, want %s", got, want)
	}
}

func TestWithTheBridgeMotionWakeLeavesTheCameraToTheApp(t *testing.T) {
	t.Parallel()
	if got := runScripts(t, motionWake(), false); !strings.Contains(got, `"camera":1`) {
		t.Errorf("in a browser motion-wake did %s, want it to open the camera", got)
	}
	if got := runScripts(t, motionWake(), true); !strings.Contains(got, `"camera":0`) {
		t.Errorf("with the Bridge motion-wake did %s, want the camera left alone", got)
	}
}

func TestWithTheBridgeThisDeviceLeavesTheCameraPermissionToTheApp(t *testing.T) {
	t.Parallel()
	if got, want := runScripts(t, thisDevice(), true), `"motion-wake":"run by the Display app"`; !strings.Contains(got, want) {
		t.Errorf("with the Bridge This device shows %s, want %s", got, want)
	}
}

// An old Display app still owns the camera and keeps the screen on, so the
// page's own scripts stand down for it too; only the Bridge's messages wait
// for an update.
func TestWithAnOldDisplayAppThePageStillLeavesTheCameraAndWakeLockToIt(t *testing.T) {
	t.Parallel()
	for _, c := range []templ.Component{wallDisplay(300), motionWake(), thisDevice()} {
		if got := runScriptsWith(t, c, true, true); !strings.Contains(got, `"calls":{"camera":0,"wakeLock":0}`) {
			t.Errorf("with an old Display app the page did %s", got)
		}
	}
}
