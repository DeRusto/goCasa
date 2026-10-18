# Research: what a web page on an HP Chromebase can do for kiosk mode, screen wake, and webcam motion detection

Ticket: DeRusto/goCasa#7 (wayfinder research). Researched 2026-10-17. Spec context: `skylite-go-spec.md` §8 "Kiosk behaviour" and Phase 4 (screensaver, idle).

## Short answer

- **Secure context first, before anything else.** Screen Wake Lock and `getUserMedia` (camera) only work over HTTPS or on `localhost`. A GoCasa instance at plain `http://192.168.x.x:8080` gets neither. The fixes are TLS (for example the existing Pangolin/reverse proxy), or the `OverrideSecurityRestrictionsOnInsecureOrigin` policy / `--unsafely-treat-insecure-origin-as-secure` flag on a managed browser.
- **Unattended full-screen on ChromeOS needs a license.** True kiosk (boot straight into one URL, no sign-in, permissions auto-granted) requires enrolling the device with Kiosk & Signage Upgrade (about $25 per device per year), or ChromeOS Enterprise or Education Upgrade. Without a license you get a signed-in user session with a fullscreen tab or an installed PWA window. That is not unattended in the strict sense, and a page cannot enter fullscreen by itself on load.
- **Screen Wake Lock works on ChromeOS** (Chrome 84+). ChromeOS honours it by default (`AllowScreenWakeLocks` defaults to true). It is released whenever the page is hidden, so the page must re-request it on `visibilitychange`. It is advisory and has no effect while the page is hidden.
- **A page cannot dim the backlight or turn the display off.** No web API does it. The page can paint a black or dimmed screensaver, or release its wake lock and let the OS idle policy blank the screen. Real backlight control exists only through device policy (`ScreenBrightnessPercent`, ChromeOS managed) or a wrapper app (Fully Kiosk on Android).
- **Camera permission can persist.** In Chrome, "Allow on every visit" persists per origin across reloads and reboots. "Allow this time" does not. On a managed device, `VideoCaptureAllowedUrls` grants it with no prompt, and ChromeOS web kiosk grants every web permission with no prompt.
- **In-browser motion detection is cheap and feasible** using frame differencing on a downscaled canvas, with frames never leaving the device. The real constraints are the always-on camera indicator/LED and not being able to turn the screen back on if the OS has blanked it.

Recommendation for the spec: treat wake lock, camera motion-wake and fullscreen as **progressive enhancements behind feature detection**. The baseline is "an idle timer plus touch to wake, rendered by the page". Note in the deployment docs that GoCasa must be served over HTTPS for any device feature to work.

---

## 1. Running a URL full-screen unattended on ChromeOS

### With a license (managed device)

- **Web app kiosk.** In the Admin console, under Devices > Chrome > Apps & extensions > Kiosks, choose "Add by URL" to add a website or PWA, and optionally set it to auto-launch. In kiosk mode "all web permissions are granted without requesting a user's consent", including camera and microphone. Use URLs that don't redirect to another origin, because "Permission requests from a different URL are automatically rejected." — https://support.google.com/chrome/a/answer/9781496
- Only one kiosk app can auto-launch per device. Kiosk settings require the device to be enrolled as a kiosk. — https://support.google.com/chrome/a/answer/9273974
- **Chrome Apps kiosks are being retired.** Chrome App support is off by default from ChromeOS 138, and admins can keep it in kiosk only until version 151. Web app / PWA / IWA kiosks are the supported path. — https://support.google.com/chrome/a/answer/3316168
- **Auto-launch managed guest session** (a full browser session that starts at boot with no sign-in) is "Only available for devices enrolled with ChromeOS Enterprise Upgrade or ChromeOS Education Upgrade." — https://support.google.com/chrome/a/answer/3316168
- **Which license.** Managing standalone ChromeOS devices needs ChromeOS Enterprise Upgrade, Education Upgrade, or Kiosk & Signage Upgrade. Kiosk & Signage supports kiosks but **not** managed guest sessions, and needs ChromeOS 104+. — https://support.google.com/chrome/a/answer/1289314, https://support.google.com/chrome/a/answer/1360534
- **Cost.** Kiosk & Signage Upgrade "starts at $25 per year, per device" (Google, June 2022). Enterprise Upgrade and Kiosk & Signage are annual. Education Upgrade is perpetual per device. — https://cloud.google.com/blog/products/chrome-enterprise/all-signs-point-chrome-os-kiosks-and-signage, https://support.google.com/chrome/a/answer/9147838
- Unmanaged ("consumer") kiosk mode no longer exists. Every ChromeOS kiosk now needs a management license. This is reported by signage vendors; Google's help pages describe kiosk only for enrolled devices, which agrees. — https://help.screencloud.com/en/articles/10110447-chrome-devices-how-to-exit-kiosk-mode-and-what-devices-do-not-work-in-kiosk-mode

### Without a license (consumer device)

- The only option is a normal signed-in user session showing the page in a fullscreen tab, or as an installed PWA window.
- The Fullscreen API needs a user gesture: "Fullscreen requests need to be called from within an event handler or otherwise they will be denied". Fullscreen also exits on navigation or tab switch. So GoCasa can offer a "go fullscreen" button, but it cannot enter fullscreen on boot. Use htmx swaps, not full navigations, to stay in fullscreen. — https://developer.mozilla.org/en-US/docs/Web/API/Fullscreen_API/Guide
- *Unverified (no primary source found):* after a reboot, consumer ChromeOS needs the user to sign in. Restoring the session or tabs, or an app's "launch at login" setting, may reopen the page, but someone has to touch the device after a power cut. Needs a test on the actual Chromebase.
- ChromeOS idle settings (Settings > Device > Power, "while idle": keep display on / turn off display / sleep) can keep the display on without any page cooperation. *Only secondary sources (2017) were found for the exact wording.*

## 2. Screen Wake Lock

- `navigator.wakeLock.request("screen")` needs a secure context and the `screen-wake-lock` permissions policy (default `self`). — https://developer.mozilla.org/en-US/docs/Web/API/Screen_Wake_Lock_API
- **No user activation is required** by the spec. Locks are released when the document is hidden or stops being fully active. The UA may release them at any time (low battery, power saving, or the OS dimming based on user presence). Locks are advisory, so a failed OS acquisition looks like success to the page. The spec is still an unstable Editor's Draft. — https://w3c.github.io/screen-wake-lock/
- Support: Chrome/Edge 84, Firefox 126, Safari 16.4, iOS Safari 18.4. On iOS 16.4–18.3 it did not work in Home Screen web apps (WebKit bug 254545). — https://raw.githubusercontent.com/mdn/browser-compat-data/main/api/WakeLock.json
- ChromeOS policy `AllowScreenWakeLocks` defaults to `true`. If an admin disables it, screen locks are downgraded to system wake locks (the screen may turn off). — https://raw.githubusercontent.com/chromium/chromium/main/components/policy/resources/templates/policy_definitions/PowerManagement/AllowScreenWakeLocks.yaml
- **Does it survive idle?** Yes, while the page stays visible. The page must re-acquire the lock after every `visibilitychange` back to visible and after every reload. It does not survive the page being backgrounded, the lid closing, or the user turning the screen off.

## 3. Can a page dim or blank the display?

- **No web API controls backlight or display power.** Neither MDN nor the W3C specs above offer one; the Wake Lock spec only *prevents* screen-off. A page can:
  - render a black or low-brightness screensaver (on an LCD the backlight stays on; it saves little power and doesn't stop burn-in on OLED), or
  - **release its wake lock** so the OS idle policy turns the display off after its own timeout. The page cannot then turn the display back on. Wake-up is by touch, mouse, or keyboard.
- **Managed ChromeOS:** `ScreenBrightnessPercent` (ChromeOS 72+) sets a fixed brightness and turns off auto-brightness. It is static: it cannot be scheduled or driven by the page. Idle-to-display-off timing is set by power-management device policy. — https://raw.githubusercontent.com/chromium/chromium/main/components/policy/resources/templates/policy_definitions/PowerManagement/ScreenBrightnessPercent.yaml
- **Fully Kiosk (Android)** exposes `fully.turnScreenOff()`, `fully.turnScreenOn()`, `fully.setScreenBrightness()` and `fully.startScreensaver()` to the page through its JS interface (PLUS license). — https://www.fully-kiosk.com/en/

## 4. Persisting camera permission across reloads and reboots

- `getUserMedia` needs a secure context. On plain HTTP, `navigator.mediaDevices` is `undefined`. UAs must ask at least once and must show a visible in-use indicator. — https://developer.mozilla.org/en-US/docs/Web/API/MediaDevices/getUserMedia
- **Chrome (desktop/ChromeOS):** the prompt offers "Allow this time" (expires when the page closes or navigates, or after 16 h) and "Allow on every visit" (persistent per origin, so it survives reloads and reboots). The household must pick the second one once. — https://developer.chrome.com/blog/one-time-permissions
- **`VideoCaptureAllowedUrls`** grants camera "without prompt" to matching origins (no `*`). It is supported on Chrome on all platforms and ChromeOS from v29, and is per-profile with dynamic refresh. It needs a managed browser or device. On Linux (the Pi) it can be set with a local JSON policy file, with no Google license. — https://raw.githubusercontent.com/chromium/chromium/main/components/policy/resources/templates/policy_definitions/Miscellaneous/VideoCaptureAllowedUrls.yaml, https://www.chromium.org/administrators/linux-quick-start/
- **ChromeOS web kiosk** auto-grants camera (see §1).
- **Insecure LAN origin:** `OverrideSecurityRestrictionsOnInsecureOrigin` (Chrome/ChromeOS/Android 69+) makes listed HTTP origins count as secure, the same as `--unsafely-treat-insecure-origin-as-secure`. — https://raw.githubusercontent.com/chromium/chromium/main/components/policy/resources/templates/policy_definitions/Miscellaneous/OverrideSecurityRestrictionsOnInsecureOrigin.yaml

## 5. In-browser motion detection (frame differencing, frames stay on device)

**Feasible, low cost.** Sketch:

1. `getUserMedia({video: {width: 320, height: 240, frameRate: 5}})` into a hidden `<video>`.
2. Every 200–500 ms, `drawImage` the frame onto a small canvas (e.g. 64×48 or 160×120) created with `getContext("2d", {willReadFrequently: true})`. That flag forces a software canvas and makes repeated `getImageData()` cheaper. — https://developer.mozilla.org/en-US/docs/Web/API/HTMLCanvasElement/getContext
3. Convert to luma, compute the mean absolute difference against the previous frame (or a slowly updated background), and count changed pixels above a threshold. Debounce over 2–3 frames.
4. On motion: leave the screensaver and re-acquire the wake lock.

**Cost estimate (arithmetic, not benchmarked):** 160×120 = 19,200 pixels; at 4 fps that is about 77k pixel comparisons per second. That is negligible next to video decode, and sits well within an all-in-one's CPU. The camera stream itself (sensor plus decode) costs more than the diff. No pixels are sent anywhere: the canvas stays in page memory, and nothing is posted to the server except, optionally, a "motion" boolean.

**Caveats:**
- The camera's in-use indicator and LED stay on for the whole time detection runs. That is required by the spec and the UI, and it is a household privacy and comfort issue. Stopping the track turns the LED off but also stops detection.
- Browsers may add fingerprinting noise to `getImageData()` under some privacy settings, which raises the noise floor. Thresholds should allow for it. — https://developer.mozilla.org/en-US/docs/Web/API/CanvasRenderingContext2D/getImageData
- Lighting changes (lamps, sunrise) cause false positives. A slowly updated background and a changed-area threshold handle most of it. Use a 3–5 s cooldown.
- Detection only runs while the page is visible and the system is awake. If the OS has turned the display off, the page cannot turn it back on (see §3). The practical design is that the **page owns the screensaver**: it holds the wake lock, shows the photo or black screensaver, and uses motion to return to the calendar. It does not rely on OS display-off.
- Spec note: the UA should mute a track when its document is not "in view". So backgrounded tabs get no frames. — https://w3c.github.io/mediacapture-main/

## 6. What degrades on other devices

| Capability | HP Chromebase, unmanaged | Chromebase + Kiosk & Signage license | Pi + Chromium (Linux) | Android tablet + Fully Kiosk | iPad (Safari / Home Screen app) |
|---|---|---|---|---|---|
| Boot straight to URL, no sign-in | No (sign-in after reboot; unverified) | Yes, web app kiosk auto-launch | Yes (autostart `chromium --kiosk URL` from the session) | Yes (Fully is the launcher) | No (Guided Access locks to an app but needs manual start) |
| Fullscreen | User-gesture button only | Yes (kiosk) | Yes (`--kiosk`) | Yes | Home Screen web app has no browser UI |
| Screen Wake Lock | Yes (84+) | Yes | Yes (Chromium 84+); behaviour depends on compositor screen blanking (unverified) | Fully has its own "Keep Screen On"; the WebView supports wake lock | iOS 18.4+ (Home Screen apps broken before 18.4) |
| Page-controlled dim/off | No | Static brightness via policy only | No (OS tools such as DPMS/`wlr-randr`/HDMI-CEC, outside the browser) | Yes, via `fully.*` JS API (PLUS) | No |
| Camera permission persists | "Allow on every visit" | Auto-granted in kiosk | `VideoCaptureAllowedUrls` via `/etc/chromium/policies/managed/*.json` | "Enable Webcam Access" (PLUS, HTTPS only) | Weak: re-prompts are common and Home Screen apps re-prompt on route changes; only "Allow" in Settings > Safari > Camera helps (WebKit bugs, not Apple docs) |
| In-page motion detection | Yes | Yes | Yes (Pi 4/5 CPU fine at 160×120) | Yes, or use Fully's built-in visual motion detection plus `onMotion` | Possible, but fragile because of permission re-prompts |

Sources for the table not already cited: Fully Kiosk feature list, JS API and pricing (PLUS is a one-time 8.90 EUR / 10.99 USD per device) — https://www.fully-kiosk.com/en/ ; iOS camera re-prompt reports — https://bugs.webkit.org/show_bug.cgi?id=215884, https://developer.apple.com/forums/thread/788518 .

## Open questions for a later decision ticket

1. **TLS on the LAN:** will GoCasa always be reached over HTTPS (Pangolin or a local reverse proxy with a real cert)? Without it, no wake lock and no camera on any device unless each browser gets an insecure-origin override. This affects deployment docs (§11).
2. **License or not for the Chromebase:** pay about $25 per year for Kiosk & Signage (true unattended boot, auto-granted camera), or accept a signed-in session with "Allow on every visit" plus a manual fullscreen tap after reboots?
3. **Screensaver ownership:** the page owns the screensaver (holds the wake lock, black or photo screen, never real display-off). Or night-time display-off is left to the OS/device, accepting that motion cannot wake it. Can a page-driven "night mode" (black screen on a schedule) stand in for real display-off?
4. **Camera opt-in UX:** motion-wake must be off by default, with a per-device toggle and a visible "camera active" note in the UI given the always-on LED. Where should that setting live: per device (localStorage) or in household settings?
5. **Hardware checks still to do on the actual Chromebase:** what happens after reboot without a license; whether acquiring a wake lock or getting a touch undims an OS-dimmed screen; the webcam's frame-rate and CPU cost.
