# Research: what GoCasa can learn from Kiosk Satellite

Researched 2026-11-23 against [jxlarrea/kiosk-satellite](https://github.com/jxlarrea/kiosk-satellite) at `d78aac9` ("Release 2026.11.29"), and GoCasa `main` at `90daff5`. Builds on `docs/research/kiosk-dimming-and-motion-options.md` (display-off, Fully Kiosk, motion sources) and `docs/research/home-assistant-and-android.md` (HA add-on, WebView APK), which are not repeated here. Also checked against the Display app planning in [DeRusto/gocasa-android](https://github.com/DeRusto/gocasa-android) (issues #3, #4, #8, #10, #11, #13).

Sources are the project's own code and docs at that commit. Links use the short form `KS:path#Lnn`, meaning `https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/path#Lnn`. Claims I couldn't tie to a source are marked **unverified**.

## Short answer

- **What it is:** a Flutter Android app (a WebView plus native Kotlin) that turns a tablet into a Home Assistant wall kiosk. It has voice, music, an Immich screensaver, camera motion and face wake, a calendar, alarms, remote admin and fleet sync ([KS:README.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/README.md)). Most of its surface is voice, audio and HA plumbing, which GoCasa doesn't need.
- **License: CC BY-NC-ND 4.0** ([KS:LICENSE](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/LICENSE)). **Borrow ideas, never code.** GoCasa is AGPLv3, and NoDerivatives plus NonCommercial can't be combined with it. Only the plugin SDK interfaces are Apache-2.0, and GoCasa has no use for them.
- **Best ideas for GoCasa itself (all fit pure Go, a browser client and no cloud):**
  1. **Motion-wake that ignores light changes.** GoCasa's in-page detector uses fixed thresholds on raw pixel deltas. Kiosk Satellite subtracts the frame's mean change, vetoes same-signed change as lighting, learns each cell's noise for dark rooms, and ignores motion for a moment after the app changes the room's light itself. This is a small JS change with a large cut in false wakes. **High.**
  2. **Wall page that recovers when the server or proxy is down.** Kiosk Satellite retries a failed or 5xx page load with backoff (5 s, then up to 60 s). GoCasa's Wall display navigates and reloads blindly, so a reload during an upgrade can strand the tablet on a browser error page. Small. **High.**
  3. **Immich "crop around faces".** Move the crop to Immich's face boxes instead of the centre. This can be done at sync time, so it keeps GoCasa's "never call upstream at request time" rule. Medium effort. **High.**
  4. **Immich "exclude tags"** (for example a `kiosk-hide` tag). **Medium.**
- **Best ideas for the Display app (gocasa-android):** its JS bridge contract (promise-based, never rejects, `CustomEvent` signals), its crash fuse that hands the Home role back, a camera frame watchdog, and a self-update scheme that can point at a folder on your own server. GoCasa could serve that folder itself. Kiosk Satellite independently lands on the same Android design the Display app already chose (Home-app alias, `showWhenLocked`/`turnScreenOn`, camera foreground service, device admin), which is useful confirmation.
- **Use Kiosk Satellite as GoCasa's shell?** Only for households that already run Home Assistant: setup refuses to continue without a working HA URL and token. A small `window.kioskSatellite` branch in GoCasa would give real screen-off. Note that its analytics are **on by default**. **Medium.**

---

## 1. What Kiosk Satellite is, and its license

- **Stack:** Flutter/Dart UI (`app/lib/`, 454 Dart files), Kotlin native layer (`app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/`), `flutter_inappwebview` for the dashboard, ONNX Runtime for wake word and vision ([KS:README.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/README.md), "Built with these projects").
- **Needs Home Assistant.** "You need Android 7.0 or newer, a reachable Home Assistant instance and a long-lived access token" ([KS:README.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/README.md), Get started). The setup wizard's step 1 fails without a URL and token that validate ([KS:app/lib/ui/setup_screen.dart#L434-L457](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/ui/setup_screen.dart#L434-L457)).
- **License:** CC BY-NC-ND 4.0: share verbatim with attribution, no commercial use, and no distributing modified material ([KS:LICENSE](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/LICENSE)). The plugin exception lets independent plugins talk to an *unmodified* app through the SDK. It explicitly does not allow "copying application implementation code" ([KS:PLUGIN-EXCEPTION.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/PLUGIN-EXCEPTION.md)). The SDK interfaces under `app/android/app/src/main/java/me/jxl/kiosk/plugins/` are Apache-2.0.
- **What that means for GoCasa (AGPLv3, `LICENSE`):** no code, shaders, assets or verbatim text from Kiosk Satellite in GoCasa or gocasa-android. Copyright covers expression, not ideas or algorithms described in prose, so reimplementing a *technique* from this report in our own code is the same clean-room stance the spec takes towards SkyLite-UX (`docs/skylite-go-spec.md` §3.6). Write from this report, not with the Kiosk Satellite source open. This is not legal advice.

## 2. Ranked recommendations

| # | Idea | Where in Kiosk Satellite | GoCasa today | Benefit | Effort | Value |
|---|---|---|---|---|---|---|
| 1 | Motion-wake that rejects lighting changes | `CameraMotion.kt`, `motion_manager.dart` | Fixed raw thresholds (`internal/web/screensaver.templ` `motionWake`) | Far fewer false wakes from photo crossfades, lamps, TV flicker; works in dim rooms | Small (page JS) | **High** |
| 2 | Retry with backoff instead of a stuck error page | `browser_manager.dart` | Blind `location.reload()` / `location.assign()` | A Wall display survives upgrades and proxy 502s on its own | Small (page JS) | **High** |
| 3 | Immich crop around faces | `docs/immich.md`, `photo_frames.dart`, `immich_manager.dart` | Fill crops the centre; zoom drifts randomly | Heads stay in frame on cropped and zooming photos | Medium (sync + template) | **High** |
| 4 | Immich exclude tags | `docs/immich.md` Filters | Exclude albums or people, favourites, rating | One tag hides a photo from the wall with no album curation | Small–medium | Medium |
| 5 | `window.kioskSatellite` branch | `docs/js-api.md`, `user_script.dart` | None (a `window.fully` branch was proposed earlier) | Real screen-off in Night mode for HA households on Kiosk Satellite | Small | Medium |
| 6 | Bridge contract for the Display app | `docs/js-api.md` | Open question (gocasa-android #11) | A tested shape for page ↔ app messages | Design only | Medium |
| 7 | Display app robustness patterns | `HomeFuse.kt`, `CameraMotion.kt` watchdog, `WakeActivity` | Not yet specified (gocasa-android #3) | A Home-app kiosk that can't brick the tablet; camera wake that recovers | In the Display app | Medium |
| 8 | Display app updates from a folder GoCasa serves | `docs/updates.md` | Not yet specified (gocasa-android #3) | Sideloaded APKs update with no internet and no GitHub limits | Small–medium | Medium |
| 9 | Sunrise before Wake time | `docs/alarms.md` Sunrise | Planned Member display (`GLOSSARY.md`, issue #107) | A gentler Night screen to Wake time hand-off | Small in page; real brightness needs the Display app | Medium |
| 10 | Devices list with remote settings | Remote admin, `docs/fleet.md` | Device settings are cookies only | Change a tablet's settings from your phone | Medium–large | Low–medium |
| 11 | Keep awake while someone reads ("postpone on motion") | `docs/screensavers.md` Motion Detection | Camera runs only on screensaver and Dim clock | The screen doesn't go to the screensaver mid-read | Small, but camera on all day | Low |
| 12 | Pixel shift on static screens | `docs/screensavers.md` | None | Less OLED burn-in from the Dim clock | Tiny | Low |
| 13 | Stack landscape pairs on portrait screens | `docs/immich.md` | Portrait screens show a pair one photo at a time | Fuller portrait walls | Medium; touches ADR 0002 | Low |
| 14 | Slideshow progress bar, tap edges to step | `docs/immich.md` | Any tap wakes | Small nicety | Small | Low |
| 15 | Daily forecast in the calendar's Week view | `docs/calendar.md` Weather | Weather pill only | At-a-glance planning | Small | Low |

## 3. High value

### 3.1 Motion-wake that rejects lighting changes

**In Kiosk Satellite:** a coarse 16×12 luminance grid, 2 samples per cell per axis ([KS:CameraMotion.kt#L101-L109](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L101-L109)), with four defences against false wakes:

1. **Mean subtraction.** Each cell is judged on its change *minus the frame's mean change*: "a light change plus the AE compensation that follows it is, to first order, a uniform shift of the whole grid, and subtracting the mean removes it". A body is a local departure from that shift ([KS:CameraMotion.kt#L69-L80](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L69-L80), [#L1555](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L1555)).
2. **Same-sign veto.** If at least 85% of the changed cells (minimum 12) moved the same way, it's lighting, not a person: "a frame-diff of a moving body has both signs at once". On their hardware, reflected monitor flicker in a 1 lux room flagged 30–90% of the grid "at exactly 100 percent sign purity" ([KS:CameraMotion.kt#L128-L139](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L128-L139), [#L1602](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L1602)).
3. **Per-cell noise model for dark rooms.** A cell counts as changed at 4× its own learned frame-to-frame jitter, kept between 4 and 16 (of 255). The jitter is an EMA (α 0.08) fed only by quiet cells. So a dim scene's small real deltas register without bright rooms tripping on noise ([KS:CameraMotion.kt#L111-L126](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L111-L126)).
4. **Quiet after the app's own light change.** Motion is ignored for 2.5 s after "screensaver transitions, screen power, brightness" ([KS:motion_manager.dart#L84](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/motion/motion_manager.dart#L84), [#L196-L209](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/motion/motion_manager.dart#L196-L209)). It also skips 3 warm-up frames and emits at most one motion event a second ([KS:CameraMotion.kt#L166-L170](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L166-L170)).

The user docs sum it up: motion detection "filters out whole-room light shifts (such as TV flashes or turning on a lamp), and suppresses detection for a few seconds during internal app brightness and slide transitions" ([KS:docs/screensavers.md#L402-L410](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/screensavers.md#L402-L410)).

**GoCasa today:** `motionWake` in `internal/web/screensaver.templ` samples a 64×48 canvas at 4 fps and counts a pixel as changed when R+G+B moves by more than 96. More than 5% changed pixels wakes the page. The only settle time is 2 s at start. All three numbers are fixed, and the deltas are raw. That means:
- The screensaver's own photo crossfade relights the room, and the camera sees the room relit, so a dark room can wake on a slide change. *Inference from the code; not tested on a device.*
- A lamp turning on, or AE hunting, reads as a uniform shift of every pixel and wakes it.
- In a dark room, a person may move R+G+B by less than 96, so the Dim clock may never wake. *Inference; not tested.*

**For GoCasa:** reimplement the four ideas in the existing page script, about 30–40 lines and no new dependency:
- Mean-subtract the deltas.
- Apply the same-sign veto.
- Use a per-cell noise floor; the existing 64×48 grid is fine, or downsample to about 16×12.
- Ignore motion for about 2.5 s after the screensaver swaps a slide, a step the page already knows about.

Frames still never leave the page. Fit: perfect.

### 3.2 A Wall page that recovers by itself

**In Kiosk Satellite:** a main-frame HTTP failure of 500 or more gets the same retry as a network error, because "Chromium commits these as successful navigations — the proxy's 502 when Home Assistant is unreachable, a reverse proxy's 502/504 during an outage — so onReceivedError never fires and, without this path, the error body sat on screen until an app restart". Retries back off "5s → 10 → 20 → 40 → 60 and holding there", reset after a successful load, and re-navigate as soon as the network returns ([KS:app/lib/managers/browser/browser_manager.dart#L1740-L1790](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/browser/browser_manager.dart#L1740-L1790)).

**GoCasa today:** the Wall display moves between pages with plain navigations: `location.assign("/screensaver")` on idle, and `location.reload()` at Night mode boundaries, on `sse:household` and on SSE reconnect (`nightWatch` and `wallDisplay` in `internal/web/screensaver.templ`). If GoCasa is restarting (an upgrade, or a pre-migration backup), the proxy answers 502 and the browser shows that page instead. GoCasa's script is gone at that point, so nothing brings the page back until someone touches it. *The stranding is an inference from how browsers treat a navigation; not tested on a tablet.* The README's upgrade path ("Replace the image and start it") makes this a routine event, not a rare one.

**For GoCasa:** one shared helper that `fetch`es `/healthz` before any automatic navigation or reload, and on failure retries with the same kind of backoff, staying on the current page in the meantime. Under 20 lines, no server change. A Display app could also catch 5xx main-frame loads natively, as Kiosk Satellite does. Fit: perfect.

### 3.3 Immich: crop around faces

**In Kiosk Satellite:** when a photo is cropped to fill the screen, the crop moves to Immich's detected faces:
- Only the cropped axis moves. A crop across centres on the faces; a crop down puts them "a little above the middle"; and a group too tall to fit keeps the top.
- Small faces in the background don't move the crop.
- Ken Burns zooms hold still on the faces.

([KS:docs/immich.md#L81-L90](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/immich.md#L81-L90)). In the code:
- Faces shorter than 40% of the tallest are ignored.
- The faces' centre sits 40% of the way down a vertical crop.
- Each face box is grown to head and shoulders first.

([KS:app/lib/ui/photo_frames.dart#L57-L92](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/ui/photo_frames.dart#L57-L92)). Face boxes come from `GET /api/faces?id=`, stored as fractions of the image ([KS:app/lib/managers/screensaver/immich_manager.dart#L287-L292](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/screensaver/immich_manager.dart#L287-L292), [#L1594](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/screensaver/immich_manager.dart#L1594)). This needs the key's `face.read` permission. Without it, photos fall back to a centred crop ([KS:docs/immich.md#L5](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/immich.md#L5)).

**GoCasa today:** the Household's fit is "fill" (centre crop) or "blur" (fit over a blurred copy). The optional slow zoom drifts to a random `--x/--y` (`internal/web/screensaver.templ`, `drift`). Neither knows where people are, so a portrait photo on "fill" often loses heads.

**For GoCasa:** compute one focus point per photo **at sync time** and store it in `photos_cache` beside `taken_at` and `place`. Then render it as `object-position`, and aim the zoom drift at it. This keeps spec principle 3: no Immich call at request time. Costs:
- One extra request per *new* asset during sync. Kiosk Satellite already sends `withPeople` on the metadata search, but only to read *who* is in a photo ([KS:immich_manager.dart#L1042-L1074](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/screensaver/immich_manager.dart#L1042-L1074)). *Unverified:* whether that answer also carries face boxes, which would avoid the extra request.
- A migration.
- Asking for `face.read`. GoCasa's key already needs `person.read` (`internal/app/immich.go` `newImmichConn`).

The Fallback folder has no faces and keeps the centre. Fit: good.

## 4. Medium value

### 4.1 Immich: exclude tags

**In Kiosk Satellite:** "Tag the handful of photos that do not belong on a shared screen (a `kiosk-hide` tag, say) and leave the rest of the library alone, so new uploads show up without any curation step." Excluding a parent tag excludes its children. Immich's search can't exclude by tag, so the kiosk lists the assets of each excluded tag and drops them locally ([KS:docs/immich.md#L42](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/immich.md#L42), [#L57](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/immich.md#L57)). It needs `tag.read` ([KS:docs/immich.md#L5](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/immich.md#L5)), and uses `GET /api/tags` and `tagIds` in `POST /api/search/metadata` ([KS:immich_manager.dart#L1028-L1077](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/screensaver/immich_manager.dart#L1028-L1077)).

**GoCasa today:** excluded albums and people, Favorites only and Minimum rating (`internal/app/immich.go` `immichConfig`, `search`). There are no tags.

**For GoCasa:** an "Exclude tags" picker, with the subtraction done in the sync goroutine. Effort is small to medium: a picker, a `tag.read` permission note, and one list call per excluded tag. Fit: good. It's a natural sibling of Exclude people, and the Glossary's "Hide" wording for Memories would need a line on whether tags narrow Memories.

### 4.2 Running GoCasa inside Kiosk Satellite (`window.kioskSatellite`)

**In Kiosk Satellite:**
- The app injects `window.kioskSatellite` "into every page at document start", synchronously, with `platform: 'kiosksatellite'` ([KS:docs/js-api.md#L3-L21](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/js-api.md#L3-L21), [KS:app/lib/managers/js_api/user_script.dart#L5-L32](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/js_api/user_script.dart#L5-L32)).
- It offers `screenOff()`/`screenOn()` ("real display power"), `setBrightness(0..1)`, `stopScreensaver()`, `pauseScreensaver(bool)` and `setInteractionActive()` ([KS:docs/js-api.md#L31-L70](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/js-api.md#L31-L70)).
- `getScreensaverSuppressed()` "always returns true. Because the app fully owns the screen, the web page must stand down its own internal screensaver" ([KS:docs/js-api.md#L70](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/js-api.md#L70)).
- A non-HA page can be the default dashboard as a Custom Page with a full URL such as `http://192.168.1.20:8080/wall.html` ([KS:docs/custom-pages.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/custom-pages.md), "Add a page").

**For GoCasa:** an optional `if (window.kioskSatellite)` branch, like the `window.fully` branch proposed in `kiosk-dimming-and-motion-options.md` §2:
- Night mode's black screen calls `screenOff()`, and waking calls `screenOn()`.
- GoCasa then has to pick one screensaver: either turn off Kiosk Satellite's, or run GoCasa's own and call `pauseScreensaver(true)`.

Small effort. **Caveats:**
- It only helps households that already run Home Assistant (§1).
- Its analytics (Basic, Usage, Diagnostics) all default to **on** ([KS:app/lib/managers/settings/definitions.dart#L12439-L12465](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/settings/definitions.dart#L12439-L12465), [KS:docs/analytics.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/analytics.md)). That clashes with GoCasa's "no telemetry" pitch, so any docs should tell people to turn them off.
- It is free only for non-commercial use.

Value: medium, and a shell for HA homes rather than a replacement for the Display app.

### 4.3 A bridge contract for the Display app (gocasa-android #11)

Kiosk Satellite's page API is a worked example of the open question "what messages pass between GoCasa's page and the Display app":
- **Presence and identity:** a synchronous presence check with a constant platform discriminator and version.
- **Never rejects:** every method returns a promise that resolves `null` (queries) or `false` (commands) on failure ([KS:docs/js-api.md#L23-L30](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/js-api.md#L23-L30)).
- **Signals:** DOM `CustomEvent`s, with no string-eval callbacks ([KS:docs/js-api.md#L3](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/js-api.md#L3), Events at [#L192](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/js-api.md#L192)).
- **State:** one state snapshot (`getKioskState()`: screensaver, `screenOn`, modes) plus one event that carries the same object whenever anything changes ([KS:docs/js-api.md#L42-L56](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/js-api.md#L42-L56)).

Adopting that *shape* (not the code) for `window.gocasaDisplay` gives GoCasa's page a bridge it can feature-detect and that fails soft. That suits spec principle 2 ("must work with JS partially broken"). Design only.

### 4.4 Display app robustness patterns (gocasa-android #3, #8, #13)

Kiosk Satellite's manifest matches the Display app's decisions: a disabled-by-default `HOME` activity-alias, a translucent `WakeActivity` with `showWhenLocked`/`turnScreenOn`, a camera foreground service and a device-admin receiver ([KS:app/android/app/src/main/AndroidManifest.xml#L196-L246](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/AndroidManifest.xml#L196-L246)). Three things it adds that the Display app's map doesn't list yet:
- **Crash fuse for the Home role.** "a kiosk that crash-loops while it IS the home app would otherwise strand whoever is in front of the device on a black screen". After 3 chained starts that never draw a frame, it gives Home back to the stock launcher, in native code "because it must work when Dart is exactly what is broken" ([KS:HomeFuse.kt#L12-L33](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/HomeFuse.kt#L12-L33), [#L45](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/HomeFuse.kt#L45)). For the Display app, which plans to *be* the Home app (#13), this is cheap insurance.
- **Camera frame watchdog.** No frames for 10 s means a dead session. It covers silent `PENDING_OPEN` binds and OS suspension and rebinds with backoff ([KS:CameraMotion.kt#L305](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L305), [#L1442-L1460](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L1442-L1460)). This matches VACA's "camera that opens but delivers no frames" note in #8.
- **Auto-exposure hunt fallback.** 20 consecutive lighting-shaped frames (about 10 s) step the exposure ladder and restart the session, after a Galaxy Tab S6 Lite whose exposure "oscillates forever, which pins a core" ([KS:CameraMotion.kt#L145-L164](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L145-L164)).

### 4.5 Display app updates from a folder GoCasa serves

**In Kiosk Satellite:**
- It checks GitHub 20 s after launch and then every 12 h.
- It falls back to the release page when GitHub's 60 requests an hour limit refuses it.
- It picks the APK for the device's architecture.
- A **Custom Repository** is any folder holding GitHub's `releases.json` saved as is, plus the APKs. Plain `http://` works, and Android's signature check means the folder "can only ever serve a real Kiosk Satellite release".
- Android 12+ shows the system prompt for the first in-app update, and "Subsequent updates install silently".

([KS:docs/updates.md#L5-L63](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/updates.md#L5-L63), [#L83-L92](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/updates.md#L83-L92)).

**For GoCasa:** this answers gocasa-android #3's "Updates for a sideloaded APK". GoCasa is the one server every Display app can always reach, so the GoCasa binary (or a mounted folder under `/data`) could serve `releases.json` and the APK. That means no internet, no GitHub limits and no extra service. Fit: good. It's a static file route, though embedding the APK in the binary would bloat it, so prefer a folder.

### 4.6 Sunrise before Wake time (Member display)

**In Kiosk Satellite:** a sunrise alarm starts 10–30 minutes early: "a glow rises from deep red to warm white while the screen brightens from its lowest level to full, then the alarm rings on the last color" ([KS:docs/alarms.md#L67-L71](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/alarms.md#L67-L71), [#L112](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/alarms.md#L112)).

**GoCasa:** the planned Night screen is "amber until Wake time and green after it", optionally with an alarm (`GLOSSARY.md`; issues #107, #113). A sunrise ramp in the last minutes before Wake time is a natural option for that design:
- **In the page:** a CSS colour and opacity ramp. The backlight stays where it is.
- **With the Display app:** a real brightness ramp.

Raise it when the Member display is specified, not before.

## 5. Low value, or later

- **Devices list with remote settings.** Kiosk Satellite has a per-device web admin (port 2324) with settings, live screenshots and logs, plus leader/follower profiles ([KS:docs/remote-api.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/remote-api.md), [KS:docs/fleet.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/fleet.md)). GoCasa keeps device settings in cookies only (`internal/app/device.go`: `default_view`, `theme`, `wall_display`, `motion_wake`), so changing a tablet means walking to it with an Admin PIN. A server-side device registry would fix that, but gocasa-android #10 deliberately kept "no device registry or pairing". Revisit only if #3's "status back to GoCasa's admin pages" is taken up.
- **Postpone the screensaver on motion.** Keeping the camera on between sessions resets the idle timer while someone reads ([KS:docs/screensavers.md#L402-L410](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/screensavers.md#L402-L410)). GoCasa runs the camera only on the screensaver and Dim clock. Running it on every page all day is a privacy cost the household should opt into separately. Low.
- **Pixel shift.** This "periodically shifts on screen elements every minute to prevent image retention on OLED displays" ([KS:docs/screensavers.md#L22](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/screensavers.md#L22)). GoCasa's Dim clock sits still for hours. A few pixels of CSS drift a minute is tiny work and matters only on OLED panels.
- **Stack landscape pairs on portrait screens** ([KS:docs/immich.md#L104-L108](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/immich.md#L104-L108)). GoCasa deals one shared deck of single photos and portrait Photo pairs for every Wall display (`docs/adr/0002-photo-rotation-is-its-own-module.md`). A portrait screen shows a pair's photos one after the other (`internal/app/rotation.go` `screensaverStep`). Stacking landscape pairs would need a deal per orientation. Low.
- **Slideshow progress bar and tap-edges-to-step** ([KS:docs/immich.md#L29-L33](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/immich.md#L29-L33)). Small niceties. Tap-to-step conflicts with GoCasa's "any touch wakes".
- **Daily forecast in the Week view** ([KS:docs/calendar.md#L62-L64](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/calendar.md#L62-L64)). GoCasa already syncs a 6-day forecast, so this is small. Low.
- **Time-block schedule of screensaver modes**, each with its own brightness, screen-off timer and wake sources ([KS:docs/screensavers.md#L388-L400](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/screensavers.md#L388-L400)). GoCasa's single Night mode window covers the common "photos by day, dark at night" case. Low.
- **Adaptive brightness curve** (four points against lux on a log scale, from the device sensor or an HA sensor; [KS:docs/screen.md#L22-L45](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/screen.md#L22-L45)). Only a native app can set the backlight, so this is a later Display app feature (#3 lists "brightness control" as unspecified).
- **Secure-context loopback proxy.** Kiosk Satellite serves a plain-http HA from `127.0.0.1:2325` inside the app so the page becomes a secure context and gets the microphone ([KS:app/lib/managers/proxy/proxy_manager.dart#L10-L30](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/proxy/proxy_manager.dart#L10-L30)). The same trick in the Display app would turn on GoCasa's *existing* Wake Lock and in-page motion-wake over plain http. But the Display app already chose native keep-awake and native camera (#4, #8), and the origin change resets the device's cookies. Keep it as a fallback idea only.

## 6. Not a fit

| Kiosk Satellite feature | Why not for GoCasa |
|---|---|
| Voice Satellite, wake words, Voice Match, Ground Control, intercom, TTS | Native audio and ML on the device plus HA Assist; far outside a calendar dashboard and the browser client |
| Music (Sendspin, AirPlay, DLNA, Music Library, Now Playing) | Not GoCasa's domain |
| RTSP/ONVIF camera streaming, camera views, Bluetooth proxy | Turns the tablet camera into a network camera, the opposite of GoCasa's "frames never leave the page" |
| Face, hand-gesture and person detection | Needs on-device ML models (ONNX) or special hardware; motion is enough for wake, and §3.1 makes it better |
| Plugin Manager | Spec §2 non-goal: "Plugin system — integrations are compiled in" |
| "Pause dashboard during screensaver" ([KS:docs/optimizations.md#L20-L54](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/optimizations.md#L20-L54)) | Already true by design: GoCasa's screensaver is its own page, so nothing renders behind it |
| Keep HA connected, filter dashboard updates | HA-frontend specific |
| At a Glance (HA entity states on the screensaver) | Needs a live HA connection from the page; GoCasa's screensaver already shows its own agenda and weather |
| Calendar view of HA calendars | GoCasa's calendar already does more: Member lanes, diagonal stripes for shared events (`internal/web/calendar.templ` `eventFill`), Edit scopes |
| Remote admin live screenshots and logs | Belong to a native app on the device, not a server-rendered page |
| Analytics (on by default) | GoCasa promises no telemetry |

## 7. Not verified

- That GoCasa's fixed-threshold motion-wake actually false-wakes on photo crossfades, or misses people in a dark room, on a real tablet (§3.1 is an inference from the code).
- That a reload or navigation during a GoCasa restart leaves a browser-based Wall display stuck on a 502 page (§3.2 is an inference; Kiosk Satellite's comment describes the same thing in WebView).
- Whether Immich's metadata search can return face boxes inline, so face-aware crops could skip a per-asset `GET /api/faces` (§3.3).
- That Kiosk Satellite works as a GoCasa shell end to end: a GoCasa URL as a Custom Page, cookies sticking, SSE through the WebView, and `screenOff()` from a non-HA origin. Read from its docs and setup code only; not installed (the brief says not to run it).
- Kiosk Satellite's claims about its own behaviour on devices (silent updates on Android 12+, power numbers in `optimizations.md`) are the vendor's own measurements.

## Sources

Kiosk Satellite at `d78aac9` (all `https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/…`):
- `README.md`, `LICENSE`, `PLUGIN-EXCEPTION.md`
- `docs/screensavers.md`, `docs/immich.md`, `docs/calendar.md`, `docs/js-api.md`, `docs/custom-pages.md`, `docs/updates.md`, `docs/alarms.md`, `docs/screen.md`, `docs/optimizations.md`, `docs/remote-api.md`, `docs/fleet.md`, `docs/analytics.md`
- `app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt`, `HomeFuse.kt`; `app/android/app/src/main/AndroidManifest.xml`
- `app/lib/managers/motion/motion_manager.dart`, `app/lib/managers/browser/browser_manager.dart`, `app/lib/managers/screensaver/immich_manager.dart`, `app/lib/ui/photo_frames.dart`, `app/lib/managers/js_api/user_script.dart`, `app/lib/managers/proxy/proxy_manager.dart`, `app/lib/managers/settings/definitions.dart`, `app/lib/ui/setup_screen.dart`

GoCasa (this repo, `90daff5`): `README.md`, `GLOSSARY.md`, `docs/skylite-go-spec.md`, `docs/adr/0002-photo-rotation-is-its-own-module.md`, `internal/web/screensaver.templ`, `internal/web/calendar.templ`, `internal/app/immich.go`, `internal/app/rotation.go`, `internal/app/device.go`, issues #107 and #113.

gocasa-android: issues [#3](https://github.com/DeRusto/gocasa-android/issues/3), [#4](https://github.com/DeRusto/gocasa-android/issues/4), [#8](https://github.com/DeRusto/gocasa-android/issues/8), [#10](https://github.com/DeRusto/gocasa-android/issues/10), [#11](https://github.com/DeRusto/gocasa-android/issues/11), [#13](https://github.com/DeRusto/gocasa-android/issues/13).
