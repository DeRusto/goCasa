# Research: kiosk options for real display dimming / display-off and motion wake

Researched 2026-10-17. Spec context: `docs/skylite-go-spec.md` §8 "Kiosk behaviour" (dim/idle to screensaver). Builds on `docs/research/chromebase-kiosk-capabilities.md` (branch `research/chromebase-kiosk-capabilities`), which established: no web API dims or turns off the display; Wake Lock and `getUserMedia` need HTTPS; ChromeOS unattended kiosk needs a Kiosk & Signage (about $25/device/year) or Enterprise/Education licence; in-page camera motion detection is feasible. None of that is repeated here. This note looks at what sits *under* the page.

## Short answer

- **HP Chromebase, keep ChromeOS:** real display-off and wake are reachable through a **small companion Chrome extension** that uses `chrome.power`. `requestKeepAwake("display")` / `releaseKeepAwake()` decide whether the OS idle timer may turn the screen off, and `chrome.power.reportActivity()` (ChromeOS 113+) "awake[s] the screen from a dimmed or turned off state". The page talks to the extension via `externally_connectable` messaging, and Google documents adding extensions to web-app kiosks. ChromeOS cannot turn the display off *immediately* on command; it turns it off when the idle `ScreenOff` delay expires, so night mode means "stop holding keep-awake, and the screen goes off after the configured delay". Brightness is static policy only (`ScreenBrightnessPercent`).
- **Android + Fully Kiosk:** the most complete off-the-shelf answer. Real screen on/off and brightness from the page's JS (`fully.turnScreenOff()`, `fully.setScreenBrightness()`) and from a REST API, plus native camera motion detection with "Turn Screen On on Motion". 8.90 EUR / 10.99 USD one-time per device. Home Assistant has an official integration.
- **Linux box (Pi, or the Chromebase reflashed) + Chromium kiosk:** full control with a tiny local agent: `wlopm`/`wlr-randr` (Wayland), `xset dpms force off` (X11), `/sys/class/backlight` (internal panels), `ddcutil` (DDC/CI monitors), `cec-ctl` (TVs). Motion comes from a PIR on GPIO (no camera) or `motion` on a USB webcam. More setup; no recurring cost.
- **Home Assistant:** a PIR or mmWave sensor (for example LD2410 via ESPHome, which also detects people who are standing still) drives any of the above. This is the best wake source for privacy and reliability, but it adds a dependency.
- **GoCasa's role:** GoCasa can own the *schedule* (night hours, "display should be off") and publish it over the SSE stream it already has. A per-platform agent applies it. **Motion wake should stay local on the device**, so waking never depends on a server round trip.

---

## 1. HP Chromebase on ChromeOS

### Hardware

- HP has one ChromeOS Chromebase: the **HP Chromebase 21.5" All-in-One (22-aa0xxx)**. It has a 21.5" FHD touch panel that pivots 90° to portrait, Intel 10th-gen (Comet Lake) Core i3 / Pentium Gold, and a **5 MP HP True Vision privacy camera with a physical switch that cuts the video feed**. — https://www.hp.com/us-en/shop/pdp/hp-chromebase-all-in-one-22-aa0050t-2z1z3av-1, https://support.hp.com/lamerica_nsc_carib-en/document/c08044565
- Google lists the HP "Chromebase 21.5 inch All-in-One Desktop" with **automatic updates until Jun 2030**. — https://support.google.com/chrome/a/answer/6220366
- Board name **DOOLY** (Comet Lake). MrChromebox lists both RW_LEGACY and **UEFI Full ROM** firmware support. Write protection is disabled via CR50 (SuzyQ) or a jumper. So the unit **can run mainstream Linux**, at the cost of wiping ChromeOS and flashing firmware. — https://docs.mrchromebox.tech/docs/supported-devices.html
- **ChromeOS Flex** is pointless here. Google supports Flex only on non-ChromeOS hardware, and on ChromeOS devices only unofficially after they stop getting updates ("might be able to install ... but Google won't support"). The Chromebase gets real ChromeOS until 2030, and Flex needs the same licences for kiosk anyway. — https://support.google.com/chromeosflex/answer/11543105

### Policies relevant to brightness and display power (managed device)

| Policy / setting | What it does | Source |
|---|---|---|
| `PowerManagementIdleSettings` (ChromeOS 35+) | Separate AC/battery delays in ms for `ScreenDim`, `ScreenOff`, `IdleWarning`, `Idle`, plus `IdleAction` (Suspend/Logout/Shutdown/DoNothing). "If the delay is set to zero, Google ChromeOS won't take the corresponding action." ScreenDim ≤ ScreenOff ≤ Idle. | https://raw.githubusercontent.com/chromium/chromium/main/components/policy/resources/templates/policy_definitions/PowerManagement/PowerManagementIdleSettings.yaml |
| Kiosk power settings (Admin console) | Configured after enrolling as kiosk: idle timeout, action on idle, screen dim/off timeouts per AC/battery, and **"Allow App to Manage Power"** under Apps & extensions > Kiosks (lets the kiosk app's power requests apply). | https://support.google.com/chrome/a/answer/3316168 |
| **Device sleep mode** | Weekly sleep/wake schedule plus a "Re-sleep delay" for manually woken devices. Needs Action on idle = Do nothing. This is *system* sleep, not display-off, so the page and its SSE stop. | https://support.google.com/chrome/a/answer/3316168 |
| `ScreenBrightnessPercent` (ChromeOS 72+) | Sets an initial brightness and turns auto-brightness off. "users can change it". Dynamic refresh, but there's no schedule, so changing it means an Admin console edit (or the Admin SDK; not researched). | https://raw.githubusercontent.com/chromium/chromium/main/components/policy/resources/templates/policy_definitions/PowerManagement/ScreenBrightnessPercent.yaml |
| Scheduled reboot | Daily/weekly/monthly at a set time; skipped within 1 h of boot. Useful for hygiene, not for display control. | https://support.google.com/chrome/a/answer/3316168 |
| `AllowScreenWakeLocks` | Default true (covered in prior research). | — |

No scheduled display-on/off policy and no brightness schedule exist. I searched the Chromium policy templates and Google's device-policy article and found none.

What "screen off" means at OS level: powerd dims "to 10% of its maximum level ... and then turned off". Default idle delays on AC are dim 7 min, screen off 7.5 min, suspend 8.5 min. **Video activity** (15+ updates of at least 333×250 within a second in a visible tab) blocks dimming and screen-off, so an animated photo screensaver will keep the screen on whatever the timeouts are. — https://chromium.googlesource.com/chromiumos/platform2/+/HEAD/power_manager/docs/screen_brightness.md, https://chromium.googlesource.com/chromiumos/platform2/+/master/power_manager/docs/inactivity_delays.md . *Unverified:* whether "turned off" on this panel means backlight power-down. The doc doesn't say, but "turned off" after a dim stage strongly implies it.

### Extension / app APIs

- **`chrome.power`** (extensions, `power` permission): `requestKeepAwake("display"|"system")`, `releaseKeepAwake()`, and **`reportActivity()` (Chrome 113+, ChromeOS only)**: "Reports a user activity in order to awake the screen from a dimmed or turned off state or from a screensaver." It has no "turn display off now" or brightness method. — https://developer.chrome.com/docs/extensions/reference/api/power
- **`chrome.system.display`**: `setDisplayProperties` and most methods are "only available to ChromeOS Kiosk apps". Properties cover bounds, mode, zoom, rotation, overscan and mirroring. It has **no brightness or power control**. — https://developer.chrome.com/docs/extensions/reference/api/system/display
- **Page ↔ extension:** a website/PWA can message an extension listed with `externally_connectable` using `chrome.runtime.sendMessage(extensionId, ...)`. Google documents this for "managed user sessions, Managed Guest Sessions (MGS), or Kiosk mode". Kiosk extensions are added under Devices > Chrome > Apps & extensions > Kiosks, self-hosted or from the Web Store. — https://developer.chrome.com/docs/iwa/connect-to-extensions, https://support.google.com/chrome/a/answer/9781496
- **Camera inside the extension (optional):** MV3 offscreen documents support `USER_MEDIA` ("interact with media streams from user media (e.g. `getUserMedia()`)"), so motion detection could live in the extension instead of the page. — https://developer.chrome.com/docs/extensions/reference/api/offscreen . *Unverified:* how camera permission is granted to an offscreen document in kiosk.

### Resulting design on the Chromebase

1. Set kiosk power policy: `ScreenOff` short (e.g. 60–120 s), `IdleAction` = DoNothing, "Allow App to Manage Power" on.
2. Companion extension (around 30 lines): on page message `awake`, call `requestKeepAwake("display")`. On `sleep`, call `releaseKeepAwake()`. On `wake`, call `reportActivity()` then `requestKeepAwake("display")`.
3. The page (already running in-page motion detection from the prior research) sends `wake` on motion and touch. GoCasa's schedule or idle logic sends `sleep`. The page must also stop any ≥15 fps animation, or powerd's video-activity rule keeps the screen on.

Hardware checks still needed (*unverified*): (a) whether the page stays "visible" and the camera keeps delivering frames after powerd turns the screen off. If not, move detection into the extension's offscreen document, or use an external sensor. (b) Whether `reportActivity()` from a kiosk extension wakes the internal panel as documented. (c) Unlicensed fallback: in a signed-in consumer session the same extension should work, because `chrome.power` has no kiosk restriction. But the idle delay is then whatever the consumer Settings > Power "While inactive and plugged in" offers, and Google's consumer help only documents the "Keep display on" choice — https://support.google.com/chromebook/answer/3420029 .

- **Screen control:** true display-off by OS idle timer; brightness static.
- **Motion wake:** built-in webcam (page or extension) → `reportActivity()`.
- **Setup:** moderate (enrol, kiosk policy, publish a private/unlisted extension).
- **Recurring cost:** Kiosk & Signage licence (prior research: about $25/device/year).
- **Privacy:** camera always on, LED lit. The physical privacy switch kills wake.
- **GoCasa changes:** the page sends `sleep`/`wake` messages if `chrome.runtime` and the extension ID are present (feature-detect). GoCasa serves the extension ID in config. HTTPS is still required.

## 2. Android + Fully Kiosk Browser

- **JS interface (PLUS):** `fully.turnScreenOn()`, `fully.turnScreenOff([keepAlive])`, `fully.forceSleep()`, `fully.setScreenBrightness(level)` ("-1 for default"), `fully.getScreenOn()`, `fully.startScreensaver()`/`stopScreensaver()`, `fully.bind('screenOn'|'screenOff'|'onMotion', ...)` (onMotion "Max. one per second"). The JS interface, Remote Admin and Scheduled Sleep request **Device Administrator** permission, which is used to turn the screen off. — https://www.fully-kiosk.com/en/
- **REST API:** `http://<ip>:2323/...?password=...` with `/screenOn`, `/screenOff`, `/forceSleep`, `/triggerMotion`, `/startScreensaver`, `/stopScreensaver`, and `/setStringSetting?key=...&value=...`. The brightness key `screenBrightness` appears in the docs' JS sample. The full REST URL for it is my inference. — https://www.fully-kiosk.com/en/
- **Motion detection (PLUS):** visual (camera) and acoustic (mic). Settings include "Detector Sensitivity" (0–100, "best at 90-95"), "Turn Screen On on Motion", "Exit Screensaver on Motion", "Turn Screen Off in Darkness", "Screen Off Timer" and "Screensaver Timer". — https://www.fully-kiosk.com/en/
- **Caveats from the vendor:** "When the screen is off Android Webview will always stop all scripting and networking after a short time". Screen off "may suspend many devices completely" (use "Prevent from Sleep while Screen Off"). Some Samsung Android 11+ devices kill apps while the screen is off. Fire OS 5 stops working with visual motion detection plus screen off. Once the screen is off, taps usually can't wake it; the vendor suggests a 0-brightness screensaver instead. — https://www.fully-kiosk.com/en/
- **Cost:** PLUS 8.90 EUR / 10.99 USD per device, one-time lifetime licence. — https://www.fully-kiosk.com/en/
- **Home Assistant:** official `fully_kiosk` integration (needs PLUS and Remote Admin plus password). It exposes screen on/off, screen brightness, screensaver, screen-off timer, motion detection, a camera entity (only while motion detection is on), plus `load_url`/`set_config` actions. Local polling. Fully also has its own MQTT (PLUS). — https://www.home-assistant.io/integrations/fully_kiosk/

- **Screen control:** true screen off (Device Admin) and real brightness.
- **Motion wake:** native camera detection, independent of the page (the page is frozen while the screen is off).
- **Setup:** low. **Recurring:** none.
- **Privacy:** camera (or mic) always on.
- **GoCasa changes:** optional `if (window.fully)` branch calling `fully.turnScreenOff(true)` for night mode. Or GoCasa/HA calls the REST API (GoCasa would then store device IP plus Remote Admin password, encrypted like other integration secrets in spec §9).

## 3. Raspberry Pi / any small Linux box + Chromium kiosk

### Turning the display off and on

| Method | Applies to | Command / interface | Notes | Source |
|---|---|---|---|---|
| `wlr-randr` | Wayland (Pi OS default labwc) | `wlr-randr --output HDMI-A-1 --off` / `--on` | Documented by Raspberry Pi. Disables the output (the layout changes), not a DPMS-style blank. | https://www.raspberrypi.com/documentation/computers/configuration.html |
| `wlopm` | Wayland compositors with `wlr-output-power-management-unstable-v1` | `wlopm --off '*'` / `--on '*'` | Turns the screen off "without disabling the logical output, somewhat like DPMS". Protocol supported by labwc, Wayfire, Sway, Hyprland, river and others; **not** Cage, Weston, Mutter or KWin. | https://man.archlinux.org/man/wlopm.1.en.txt, https://wayland.app/protocols/wlr-output-power-management-unstable-v1 |
| `xset` | X11 | `xset dpms force off` / `on`; `xset -dpms`; `xset s off` | "forces the server to immediately switch to the DPMS state specified". | https://xorg.freedesktop.org/archive/current/doc/man/man1/xset.1.xhtml |
| sysfs backlight | Internal panels (DSI touch screens, the Chromebase's eDP panel if reflashed to Linux) | `/sys/class/backlight/*/brightness` (0..max_brightness), `bl_power` (0 = on, 4 = powerdown) | True backlight dimming and power. | https://www.kernel.org/doc/Documentation/ABI/stable/sysfs-class-backlight |
| `ddcutil` | External monitors with DDC/CI over HDMI/DP | e.g. `ddcutil setvcp 10 30` (brightness) | "does not support laptop displays". Needs `/dev/i2c-n`. VCP code 0x10 = brightness comes from MCCS; not stated on the ddcutil home page. | https://www.ddcutil.com/ |
| `cec-ctl` (v4l-utils) | TVs over HDMI-CEC | `cec-ctl -d/dev/cecX --to 0 --standby` / `--image-view-on` | Kernel CEC framework. `cec-client` (libCEC) also supports the Pi. Exact `cec-client` command strings are not in libCEC's docs (*unverified*). | https://manpages.debian.org/trixie/v4l-utils/cec-ctl.1.en.html, https://github.com/Pulse-Eight/libcec |
| `vcgencmd display_power` | Legacy firmware display stack | — | *Unverified:* not documented in current Raspberry Pi docs (KMS era). Don't rely on it. | https://www.raspberrypi.com/documentation/computers/os.html |
| Screen blanking | Pi OS desktop | Control Centre or `raspi-config` > Display Options > Screen Blanking; default 600 s | Idle blanking only. Disable it when an agent owns the display. | https://www.raspberrypi.com/documentation/computers/configuration.html |

### Motion wake

- **PIR on GPIO:** `gpiozero.MotionSensor(pin)` with `when_motion` / `when_no_motion` callbacks. No camera. A PIR only sees *movement*, so someone sitting still reads as "no motion". — https://gpiozero.readthedocs.io/en/stable/api_input.html
- **USB webcam + `motion`:** `on_event_start` / `on_event_end` run a script; `event_gap` is the "seconds of no motion detected that triggers the end of an event"; `threshold` is the changed-pixel count. motionEye is a web UI on top of `motion`. — https://motion-project.github.io/motion_config.html, https://github.com/motioneye-project/motioneye
- **In-page detection** (prior research) also works, but stops being useful once the display is truly off only if the compositor stops the page. *Unverified* on Wayland with `wlopm`. An output that is powered off but still enabled should keep rendering.

### Exposing it to the page

- **Option A, page calls a local endpoint:** an agent on `127.0.0.1:<port>` with `POST /display {on|off|brightness}`. An HTTPS page may fetch `http://127.0.0.1` (loopback counts as potentially trustworthy, so it isn't blocked as mixed content — https://w3c.github.io/webappsec-secure-contexts/#is-origin-trustworthy). But **Chrome's Local Network Access permission** (prompts from Chrome 142) gates public-site → local/loopback requests, and Google says it will extend to "from a local server to localhost". Pre-grant with `LocalNetworkAccessAllowedForUrls` / `LoopbackNetworkAllowedForUrls` in `/etc/chromium/policies/managed/*.json`. — https://developer.chrome.com/blog/local-network-access, https://chromeenterprise.google/policies/local-network-access-allowed-for-urls/, https://chromeenterprise.google/policies/loopback-network-allowed-for-urls/
- **Option B, the agent subscribes to GoCasa (preferred):** the agent opens an outbound connection to GoCasa's existing `/events/stream` (SSE) and applies `display` events. Motion is handled locally (PIR → `wlopm --on` immediately) and optionally POSTed to GoCasa so the page leaves the screensaver. This needs no browser permission, CORS or LNA work, and it also works when the browser is wedged.

- **Screen control:** true backlight/power (sysfs, DPMS, wlopm, DDC/CI, CEC), depending on the display.
- **Motion wake:** PIR (private) or webcam via `motion`.
- **Setup:** highest (OS image, compositor choice, autostart Chromium, agent as a systemd unit, wiring).
- **Recurring:** none.
- **GoCasa changes:** a `display` SSE event and an optional motion POST endpoint (see §5).

## 4. Home Assistant–driven

- **Sensors:** a PIR via ESPHome/Zigbee, or **mmWave**, e.g. HI-LINK LD2410 in ESPHome. It exposes `has_target`, `has_moving_target` and `has_still_target`, with per-distance-gate still-energy thresholds, so it **holds presence for someone standing still reading the calendar**. That is the main weakness of PIR solved. — https://esphome.io/components/sensor/ld2410/
- **Actuation:** HA automation on presence → Fully integration `switch`/`light` (Android), or `rest_command` to a Linux agent's HTTP endpoint (`url`, `method`, `payload`), or HA's own CEC/other integrations. — https://www.home-assistant.io/integrations/rest_command/, https://www.home-assistant.io/integrations/fully_kiosk/
- ChromeOS: HA cannot reach `chrome.power` directly. It would have to go HA → GoCasa → page → extension (*no primary source; design inference*).
- **Privacy:** best (no camera). **Cost:** sensor hardware only. **Effort:** moderate if HA already runs, high otherwise.
- **GoCasa changes:** none required. Optionally accept an HA webhook or call so the page leaves the screensaver as soon as presence starts.

## 5. Server-assisted: GoCasa owns the schedule

What belongs on the server vs the device:

| Concern | Owner | Why |
|---|---|---|
| Night hours, "display off now", per-device brightness level | **GoCasa** (household settings, spec §8 screensaver config) | One place to edit. It already pushes over SSE. |
| Idle timeout after last touch | **Device/page** | Touch events are local; a round trip adds lag and fails when the server is down. |
| Motion → wake | **Device** (local sensor or agent) | Waking must work offline and instantly. Report to the server is optional. |

Minimal surface for GoCasa (design proposal, not in spec yet):
- SSE event `display` with `{state: "on"|"off"|"dim", brightness?: 0-100, until?: ts}`, emitted at schedule boundaries and on "display off now" from Settings, and repeated on (re)connect so a rebooted device converges.
- `POST /api/devices/{id}/motion` (optional) so other screens and HA can react.
- A device identity (a per-device token or name in a cookie/query) so schedules can differ per screen.

Minimal agent per platform:

| Platform | Agent | Gets the schedule from | Applies it with |
|---|---|---|---|
| ChromeOS Chromebase | Companion extension (~30 lines) | The page (it already holds SSE) via `externally_connectable` | `chrome.power` keep-awake / release / `reportActivity` |
| Android | None: Fully itself | The page via `window.fully`, or GoCasa/HA via Fully REST | `fully.turnScreenOff/On`, `setScreenBrightness` |
| Linux / Pi | systemd script (~50 lines, e.g. Python with `gpiozero`) | Its own outbound SSE to GoCasa | `wlopm` / `xset` / sysfs / `ddcutil` / `cec-ctl` |
| Any + HA | HA automation | HA reads GoCasa state, or HA owns the schedule | The integrations above |

Baseline for every device stays as in the prior research: the page draws a black/dimmed overlay when told `off`/`dim`. A device with an agent gets real display-off on top.

## Comparison

| Option | Screen control | Motion wake | Setup | Recurring cost | Privacy | GoCasa changes |
|---|---|---|---|---|---|---|
| Chromebase, unmanaged, page only | Black overlay; OS idle may blank but the page can't wake it | In-page camera (only while the screen is on) | Low | None | Camera | None beyond the prior research |
| Chromebase + Kiosk licence + companion extension | **True off via OS idle `ScreenOff`**; brightness static policy | Webcam → `reportActivity()` (*hardware test pending*) | Moderate | ~$25/yr (prior research) | Camera, LED on; privacy switch kills wake | `display` SSE; page→extension messaging |
| Chromebase reflashed to Linux (MrChromebox) | True backlight via sysfs | Webcam (`motion`) or a USB/GPIO-less PIR via HA | High; loses ChromeOS updates to 2030 | None | Camera, or sensor via HA | As Linux |
| Android tablet + Fully PLUS | **True off + brightness**, page JS or REST | Native camera / mic detection | Low | 8.90 EUR one-time | Camera | Optional `window.fully` branch |
| Pi + Chromium + agent | True off (wlopm/DPMS/CEC); brightness via sysfs/DDC | PIR on GPIO or webcam | High | None | **PIR: no camera** | `display` SSE (+ motion POST) |
| Any + HA + mmWave/PIR | Via whichever actuator above | LD2410 mmWave (detects still people) or PIR | Moderate (HA required) | None | **Best** | None required |

## Recommendation

**(a) Keep the HP Chromebase.** Stay on ChromeOS and take the Kiosk & Signage licence for unattended boot. Add the companion extension: the page holds `requestKeepAwake("display")` while awake, releases it for night/idle (screen off after a short `ScreenOff` delay), and calls `reportActivity()` on motion or touch. First run the two hardware checks: does the camera keep streaming after OS screen-off, and does `reportActivity()` wake the panel. If the camera stops, put motion detection in the extension's offscreen document. Or drop camera motion and wake with touch only, or use HA presence relayed through GoCasa. Don't reflash it: ChromeOS is supported until Jun 2030.

**(b) Most flexible long-term.** Make GoCasa own the schedule (a `display` SSE event) and keep wake device-local. For new screens, prefer a **Linux box + Chromium + a ~50-line agent subscribing to GoCasa's SSE, with a PIR or LD2410 mmWave sensor**. That gives true display power, no camera, no licence and no vendor lock-in. An Android tablet + Fully PLUS is the low-effort alternative when a camera is acceptable. Keep the page's black-overlay screensaver as the universal fallback.

### Not verified from a primary source
- Whether ChromeOS "screen off" powers down this panel's backlight (implied, not stated).
- Whether in-page `getUserMedia` keeps delivering frames after ChromeOS or Wayland turns the display off.
- Consumer (unlicensed) ChromeOS idle options beyond "Keep display on".
- Camera permission for an extension offscreen document in kiosk.
- `cec-client` command strings (use `cec-ctl`, which is documented); `vcgencmd display_power` under KMS.
- The exact Fully REST URL for brightness (assembled from documented parts).
- Whether `ScreenBrightnessPercent` can be changed programmatically on a schedule (Admin SDK not researched).
