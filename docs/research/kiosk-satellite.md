# Research: what GoCasa can learn from Kiosk Satellite

Researched 2026-11-23 against [jxlarrea/kiosk-satellite](https://github.com/jxlarrea/kiosk-satellite) at `d78aac9` ("Release 2026.11.29"), and GoCasa `main` at `90daff5`. Builds on `docs/research/kiosk-dimming-and-motion-options.md` (display-off, Fully Kiosk, motion sources, the `display` SSE proposal) and `docs/research/home-assistant-and-android.md` (HA add-on, WebView APK), which are not repeated here. Also checked against the Display app planning in [DeRusto/gocasa-android](https://github.com/DeRusto/gocasa-android) (issues #3, #4, #8, #10, #11, #13).

Sources are the project's own code and docs at that commit. Every link to it is pinned to `d78aac9`. Claims I couldn't tie to a source are marked **unverified**, and my own inferences say so.

## Short answer

- **What it is:** a Flutter Android app (a WebView plus native Kotlin) that turns a tablet into a **Home Assistant** wall kiosk ([README.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/README.md)). Most of it (voice, music, HA plumbing) is outside GoCasa's scope.
- **License: CC BY-NC-ND 4.0** ([LICENSE](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/LICENSE)). **Borrow ideas, never code.**
- **Top picks for GoCasa:**
  1. **Motion-wake that ignores light changes.** A small change to the page script should cut false wakes from slide changes, lamps and TVs, and catch people in dim rooms. This needs a tablet test. **High.**
  2. **Immich "crop around faces"**, computed at sync time. **High.**
  3. **A Wall display that comes back after a server outage.** The page alone can only narrow the gap. Full recovery needs the Display app to retry failed loads, as Kiosk Satellite does. **High** for the Display app, **medium** for the page.
- **For the Display app:** its JS bridge contract, a crash fuse for the Home role, a camera frame watchdog, and updates from a folder GoCasa can serve. Its Android design matches the one the Display app already chose.
- **Kiosk Satellite as GoCasa's shell:** possible only where Home Assistant already runs. Real screen-off needs device admin and a proven wake path, so keep GoCasa's black page until then. Its analytics are on by default. **Medium.**

---

## 1. What Kiosk Satellite is, and its license

- **Stack:** Flutter/Dart UI (`app/lib/`, 454 Dart files), Kotlin native layer (`app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/`), `flutter_inappwebview` for the dashboard ([README.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/README.md), "Built with these projects"). On-device models:
  - **ONNX Runtime** for the wake word ([build.gradle.kts#L66-L72](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/build.gradle.kts#L66-L72)).
  - **MediaPipe Tasks** for vision: the hand landmarker, and BlazeFace for faces ([build.gradle.kts#L237-L242](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/build.gradle.kts#L237-L242), [FaceDetector.kt#L27](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/FaceDetector.kt#L27)).
- **Needs Home Assistant.** "You need Android 7.0 or newer, a reachable Home Assistant instance and a long-lived access token" ([README.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/README.md), Get started). The setup wizard's step 1 fails without a URL and token that validate ([setup_screen.dart#L434-L457](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/ui/setup_screen.dart#L434-L457)).
- **License:** CC BY-NC-ND 4.0 lets you share it verbatim with attribution. It forbids commercial use and forbids distributing modified material ([LICENSE](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/LICENSE)).
  - The plugin exception lets independent plugins talk to an *unmodified* app through the SDK. It explicitly does not allow "copying application implementation code" ([PLUGIN-EXCEPTION.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/PLUGIN-EXCEPTION.md)).
  - Only the SDK interfaces under `app/android/app/src/main/java/me/jxl/kiosk/plugins/` are Apache-2.0, and GoCasa has no use for them.
- **What that means for GoCasa (AGPLv3, `LICENSE`):**
  - No code, shaders, assets or verbatim text from Kiosk Satellite in GoCasa or gocasa-android. NoDerivatives plus NonCommercial can't be combined with the AGPL.
  - Copyright covers expression, not ideas or algorithms described in prose. Reimplementing a *technique* from this report in our own code is the same clean-room stance the spec takes towards SkyLite-UX (`docs/skylite-go-spec.md` §3.6).
  - Write from this report, not with the Kiosk Satellite source open. This is not legal advice.

## 2. Ranked recommendations

| # | Idea | Where in Kiosk Satellite | GoCasa today | Benefit | Effort | Value |
|---|---|---|---|---|---|---|
| 1 | Motion-wake that rejects lighting changes (§3.1) | `CameraMotion.kt`, `motion_manager.dart` | Fixed raw thresholds (`internal/web/screensaver.templ` `motionWake`) | Expected: fewer false wakes, wakes in dim rooms (needs a tablet test) | Small (page JS) | **High** |
| 2 | Immich crop around faces (§3.2) | `docs/immich.md`, `photo_frames.dart`, `immich_manager.dart` | Fill crops the centre; zoom drifts randomly | Heads stay in frame | Medium (sync, backfill, migration, page framing, JSON) | **High** |
| 3 | Recover from a failed page load (§3.3) | `browser_manager.dart`, `kiosk_screen.dart` | Blind `location.reload()` / `location.assign()` | A Wall display survives upgrades and proxy 502s | Page: small (narrows the gap). Display app: small (closes it) | **High** (Display app) / Medium (page) |
| 4 | Immich exclude tags (§4.1) | `docs/immich.md`, `immich_manager.dart` | Exclude albums or people, favourites, rating | One tag hides a photo from the wall | Medium (paginated listing per tag, fail-closed) | Medium |
| 5 | `window.kioskSatellite` branch (§4.2) | `docs/js-api.md`, `user_script.dart`, `screen_manager.dart` | None (a `window.fully` branch was proposed earlier) | Real screen-off in Night mode for HA households, once a wake path is proven | Small code; end-to-end test first | Medium |
| 6 | Bridge contract for the Display app (§4.3) | `docs/js-api.md` | Open question (gocasa-android #11) | A tested shape for page ↔ app messages | Design only | Medium |
| 7 | Display app robustness patterns (§4.4) | `HomeFuse.kt`, `CameraMotion.kt` | Not yet specified (gocasa-android #3) | A Home-app kiosk that can't brick the tablet; camera wake that recovers | In the Display app | Medium |
| 8 | Display app updates from a folder GoCasa serves (§4.5) | `docs/updates.md` | Not yet specified (gocasa-android #3) | Sideloaded APKs update with no internet | Small–medium | Medium |
| 9 | Devices list with remote settings and profiles (§5) | `docs/remote-api.md`, `docs/fleet.md` | Cookies only | Change a tablet from your phone | Medium–large | Low–medium |
| 10 | Postpone the screensaver on motion (§5) | `docs/screensavers.md` | Camera only on screensaver and Dim clock | No screensaver while someone reads | Small; camera on all day | Low |
| 11 | Pixel shift on static screens (§5) | `docs/screensavers.md` | None | Less OLED burn-in | Tiny | Low |
| 12 | Landscape pairs on portrait screens (§5) | `docs/immich.md` | Pairs shown one photo at a time | Fuller portrait walls | Medium; touches ADR 0002 | Low |
| 13 | Progress bar, tap edges to step (§5) | `docs/immich.md` | Any tap wakes | Nicety | Small | Low |
| 14 | Sunrise before Wake time (§5) | `docs/alarms.md` | #112 decided a native Night screen with no "nearly time" cue in v1 | A gentler wake-up | Display app, after v1 | Low (later) |
| 15 | Daily forecast in the Week view (§5) | `docs/calendar.md` | Weather pill only | Planning at a glance | Small (longer forecast cache for full weeks) | Low |
| 16 | Per-block screensaver schedule with wake overrides (§5) | `docs/screensavers.md` | One Night mode window | Different wake rules by time of day | Medium | Low |
| 17 | Adaptive brightness curve (§5) | `docs/screen.md` | None (a page can't set the backlight) | Comfortable brightness day and night | Display app only | Low (later) |
| 18 | Secure-context loopback proxy (§5) | `proxy_manager.dart` | n/a | Wake Lock and in-page camera over plain http in a WebView | Display app; resets cookies | Low |

## 3. High value

### 3.1 Motion-wake that rejects lighting changes

**In Kiosk Satellite:** a coarse 16×12 luminance grid, 2 samples per cell per axis ([CameraMotion.kt#L101-L109](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L101-L109)). Each frame's deltas feed two different tests ([CameraMotion.kt#L1566-L1604](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L1566-L1604)):

1. **Change count on mean-adjusted deltas.** A cell counts as changed when its delta *minus the frame's mean delta* passes its threshold: "a light change plus the AE compensation that follows it is, to first order, a uniform shift of the whole grid, and subtracting the mean removes it". A body is a local departure from that shift ([CameraMotion.kt#L69-L80](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L69-L80), [#L1555](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L1555)).
2. **Sign veto on *raw* deltas.** The code says: "Raw deltas feed the sign veto only." If at least 85% of the raw-changed cells (minimum 12) moved the same way, the frame is lighting, not a person. As the code puts it, "a frame-diff of a moving body has both signs at once". The veto must use raw deltas because subtracting the mean first would erase the uniform shift it's meant to spot. On their hardware, reflected monitor flicker in a 1 lux room flagged 30–90% of the grid "at exactly 100 percent sign purity" ([CameraMotion.kt#L128-L139](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L128-L139), [#L1595-L1604](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L1595-L1604)).

Two supporting measures:

3. **Per-cell noise model for dark rooms.** A cell's threshold is 4× its own learned frame-to-frame jitter, kept between 4 and 16 (of 255). The jitter is an EMA (α 0.08) over the *mean-adjusted* magnitude, learned only from quiet cells, so motion and global flicker don't desensitise it ([CameraMotion.kt#L111-L126](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L111-L126), [#L1585-L1592](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L1585-L1592)).
4. **Quiet after the app's own light change.** Motion is ignored for 2.5 s after "screensaver transitions, screen power, brightness" ([motion_manager.dart#L84](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/motion/motion_manager.dart#L84), [#L196-L209](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/motion/motion_manager.dart#L196-L209)). It also skips 3 warm-up frames and emits at most one motion event a second ([CameraMotion.kt#L166-L170](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L166-L170)).

The user docs: motion "filters out whole-room light shifts (such as TV flashes or turning on a lamp), and suppresses detection for a few seconds during internal app brightness and slide transitions" ([docs/screensavers.md#L402-L410](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/screensavers.md#L402-L410)).

**GoCasa today:** `motionWake` in `internal/web/screensaver.templ` samples a 64×48 canvas at 4 fps:
- A pixel counts as changed when R+G+B moves by more than 96 (raw delta).
- More than 5% changed pixels wakes the page.
- The only settle time is 2 s at start, and all three numbers are fixed.

What follows from the code (my inference; not tested on a tablet):
- The screensaver's own crossfade relights the room, and a lamp turning on shifts every pixel; either can read as motion.
- In a dark room, a person may move R+G+B by less than 96, so the Dim clock may not wake.

**For GoCasa:** reimplement the four ideas in the existing page script, about 30–40 lines and no dependency:
- Compute raw per-cell deltas on a coarse grid (the 64×48 canvas averaged down to about 16×12).
- Run the sign veto on the raw deltas.
- Count changes on mean-adjusted deltas against a per-cell noise floor.
- Ignore motion for about 2.5 s after the page swaps a slide, a step it already knows about.

Frames still never leave the page. Benefit is *expected*: test on a tablet in a dim room with the photo rotation running before calling it fixed. Fit: perfect.

### 3.2 Immich: crop around faces

**In Kiosk Satellite:** when a photo is cropped to fill the screen, the crop moves to Immich's detected faces:
- Only the cropped axis moves. A crop across centres on the faces; a crop down puts them "a little above the middle"; and a group too tall to fit keeps the top.
- Small background faces don't count.
- Ken Burns zooms hold still on the faces.

([docs/immich.md#L81-L90](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/immich.md#L81-L90)). In the code:
- Faces shorter than 40% of the tallest are ignored.
- The faces' centre sits 40% of the way down a vertical crop.
- Each box grows to head and shoulders first.

([photo_frames.dart#L57-L135](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/ui/photo_frames.dart#L57-L135)).

Face boxes come from `GET /api/faces?id=`, stored as fractions of the image ([immich_manager.dart#L287-L292](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/screensaver/immich_manager.dart#L287-L292)). The answers live in a bounded in-memory cache, which is cleared when the Immich server or key changes, or once it passes 2,000 entries. The lookup tells three outcomes apart:
- **Failed** (slow or away server): not cached, so it's asked again next lap.
- **No `face.read`, or no faces found:** asked again after 30 minutes (`faceRetry` at [immich_manager.dart#L1557](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/screensaver/immich_manager.dart#L1557)).
- **Found faces:** kept until the cache is cleared.

([immich_manager.dart#L1588-L1626](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/screensaver/immich_manager.dart#L1588-L1626), [docs/immich.md#L90](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/immich.md#L90)). Without `face.read` the crop stays centred ([docs/immich.md#L7](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/immich.md#L7), [#L90](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/immich.md#L90)).

**GoCasa today:** the Household's fit is "fill" (centre crop) or "blur" (fit over a blurred copy). The optional slow zoom drifts to a random `--x/--y` (`internal/web/screensaver.templ`, `drift`). Neither knows where people are.

**For GoCasa:** a single focus point is too simple. Kiosk Satellite computes the crop alignment *and* the zoom anchor separately for each frame from three things: the photo's shape, the frame's shape, and the padded face bounds. That's what keeps a group in view. GoCasa shows photos both full-screen and as halves of a Photo pair, so the frame's shape varies too. So:
- **Store the framing inputs at sync time** in `photos_cache`, beside `taken_at` and `place`, as two boxes normalised to 0–1. Background faces (under 40% of the tallest) are dropped first. Kiosk Satellite uses the two boxes differently ([photo_frames.dart#L93-L135](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/ui/photo_frames.dart#L93-L135)):
  - **The unpadded union of the faces:** its centre is the anchor the crop and zoom aim at.
  - **The padded head-and-shoulders union** (each face grown 30% of its width on each side, 50% of its height above and 70% below, then clipped to the image): the limits the crop must keep in view.

  Store both, because the padding is uneven and clipped, so one box can't give back the other.
- **Compute in the page, per rendered frame:** the `object-position` for the crop, and the zoom drift's origin, so the zoom holds on the faces.

A centre-of-faces `object-position` alone would be a simpler approximation. It would keep a single face in view but can cut groups.

Full effort:
- **A migration** for the column(s).
- **Sync:** fetch faces for new assets. Also add a **backfill** pass over photos already cached, and a **refresh** for photos that had no faces or failed (Immich may scan them later). Keep "failed" apart from "none found", as Kiosk Satellite does, so a slow Immich doesn't freeze photos at the centre.
- **Delivery:** both the first server render and the `/screensaver/photo` JSON must carry both boxes, because the page builds later slides from that JSON (`internal/web/screensaver.templ`, the slide builder around `drift`).
- **Permission:** ask for `face.read`. GoCasa's key already needs `person.read` (`internal/app/immich.go` `newImmichConn`).

This adds **no request-time face lookup**. Note that GoCasa already fetches an Immich thumbnail at request time on a photo-cache miss (`internal/app/immich.go` `cachedPhoto`); that path is unchanged.

Kiosk Satellite sends `withPeople` on the metadata search, but only to learn *who* is in a photo ([immich_manager.dart#L1042-L1074](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/screensaver/immich_manager.dart#L1042-L1074)). *Unverified:* whether that answer also carries face boxes, which would save the per-asset request.

The Fallback folder has no faces and keeps the centre. Fit: good.

### 3.3 Recovering from a failed page load

**In Kiosk Satellite:**
- The *shell* watches every main-frame load. A 5xx answer goes to the retry path ([kiosk_screen.dart#L2684-L2693](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/ui/kiosk_screen.dart#L2684-L2693)), because "Chromium commits these as successful navigations — the proxy's 502 when Home Assistant is unreachable, a reverse proxy's 502/504 during an outage — so onReceivedError never fires and, without this path, the error body sat on screen until an app restart".
- Retries back off "5s → 10 → 20 → 40 → 60 and holding there", reset after a good load, and re-navigate after the network returns.
- Two qualifications. Retries are *scheduled* only when the **auto-reload on error** setting is on, which it is by default ([definitions.dart#L594-L597](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/settings/definitions.dart#L594-L597)); the check happens when the timer is created ([browser_manager.dart#L1774](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/browser/browser_manager.dart#L1774)). Network-return recovery is a separate path: it re-navigates a failed page after a two-second wait "for routes and DNS" ([browser_manager.dart#L2424-L2443](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/browser/browser_manager.dart#L2424-L2443)).

([browser_manager.dart#L1740-L1790](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/browser/browser_manager.dart#L1740-L1790)).

**GoCasa today:** the Wall display moves between pages with plain navigations:
- `location.assign("/screensaver")` when idle.
- `location.reload()` at Night mode boundaries, on `sse:household` and on SSE reconnect.

(`nightWatch` and `wallDisplay` in `internal/web/screensaver.templ`.) If one of these lands while GoCasa is restarting (an upgrade, or a pre-migration backup), the proxy's 502 page replaces GoCasa's. GoCasa's script is gone, so nothing brings it back until someone touches the screen and reloads. *My inference from how browsers treat a navigation; not tested on a tablet.* The README's upgrade path ("Replace the image and start it") makes restarts routine.

**What the page can and can't do:**
- **Narrow the gap (small):** before an *automatic* navigation, `fetch('/healthz')` with a short timeout and require a 200. If that fails, stay on the current page (it still renders from what it has) and retry with backoff. This avoids starting a navigation into a known outage. It does **not** close the race: `/healthz` only checks SQLite answers (`internal/app/app.go` `healthz`), and the navigation is a separate request that can still fail after the probe passes.
- **Recover after a failed navigation:** impossible from the page, because the error page has replaced it. That needs something outside the page:
  - **The Display app (best):** catch 5xx and network errors on main-frame loads and retry with backoff, as above. gocasa-android #10 already plans to re-resolve the server and keep retrying on "Can't reach GoCasa"; adding the 5xx case and the backoff completes it. Small.
  - **A service worker that serves a cached "reconnecting" page** for failed navigations. Service workers need a secure context, so this only helps HTTPS installs. *Unverified for GoCasa:* SSE and htmx behaviour behind a service worker. Medium.

## 4. Medium value

### 4.1 Immich: exclude tags

**In Kiosk Satellite:** "Tag the handful of photos that do not belong on a shared screen (a `kiosk-hide` tag, say) and leave the rest of the library alone, so new uploads show up without any curation step." Excluding a parent tag excludes its children ([docs/immich.md#L42](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/immich.md#L42), [#L57](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/immich.md#L57)). It needs `tag.read` ([docs/immich.md#L7](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/immich.md#L7)).

Immich's search can't exclude by tag, so `_assetsTagged` does one paginated `POST /api/search/metadata` per excluded tag, then drops those ids locally ([immich_manager.dart#L1220-L1275](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/screensaver/immich_manager.dart#L1220-L1275)).
- It pages in 500s and **stops after 20 pages** (10,000 assets per tag).
- At that cap it logs that "the rest" may still show.

**GoCasa today:** excluded albums and people, Favorites only and Minimum rating (`internal/app/immich.go` `immichConfig`, `search`). There are no tags. Memories come through a separate call (`internal/app/immich.go` `memories`), and the Glossary says Hide, Favorites only and Minimum rating "never narrow them".

**For GoCasa:** an "Exclude tags" picker, with the subtraction done in the sync goroutine. Two things to decide first:
- **Fail closed, with one gate.** Handling a failed tag listing isn't enough, for two reasons:
  - GoCasa deliberately keeps showing cached Immich photos when a sync fails (`GLOSSARY.md`, **Failing**), and `immich.Sync` can stop early at the album and people listing, the search or Memories (`internal/app/immich.go` `Sync`). A photo tagged `kiosk-hide` since the last good sync would keep showing.
  - An open screensaver keeps its current photo until the next step.

  So:
  - Persist an **eligibility gate**, shared by the rotation (`internal/app/rotation.go`) and the photo handler (`cachedPhoto`).
  - Open the gate only after a sync completes with the full exclusion applied. Close it on any incomplete sync while tags are excluded, and whenever the exclusion settings change.
  - While it's closed, no Immich photo shows: the Fallback folder takes over if it's on, and a note like `memories_note` says why.
  - Make the displays honour the gate too, because a server-side flag alone leaves gaps:
    - **Clear on close:** displays drop any Immich slide when the gate closes.
    - **Recheck on reconnect:** GoCasa's stream sends only a hello and heartbeats and replays nothing missed (`internal/app/live.go`), so a display that was offline must check the gate's state when it reconnects.
    - **Drop stale work:** the page awaits image decoding before it adds a slide (`internal/web/screensaver.templ`), so discard pending slide work started under an earlier gate state.
    - **Revalidate images:** Immich photos are served `private, max-age=86400` (`internal/app/immich.go`), so a browser could show a cached photo without asking. Revalidate Immich image requests (or make them uncacheable) while tags are excluded.

  People will use the tag as a privacy filter, so a wrong "show" is worse than a blank screensaver.
- **Whether excluded tags apply to Memories.** Unlike Favorites or rating, a hide tag is meant to hide, so probably yes; that needs a Glossary edit to the Memories entry. If so:
  - The tag listing must not reuse the album and people filters. Kiosk Satellite scopes its listing to the playlist's own favourite and date filters ([immich_manager.dart#L1220-L1226](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/screensaver/immich_manager.dart#L1220-L1226)), but GoCasa's Memories skip those filters.
  - The exclusion must run after picks and Memories are merged, before `replacePhotos`.

Effort: medium. A picker, one paginated, unscoped listing per tag per sync, and the fail-closed path. Fit: good.

### 4.2 Running GoCasa inside Kiosk Satellite (`window.kioskSatellite`)

**In Kiosk Satellite:**
- The app injects `window.kioskSatellite` "into every page at document start", synchronously, with `platform: 'kiosksatellite'` ([docs/js-api.md#L3-L21](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/js-api.md#L3-L21), [user_script.dart#L5-L32](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/js_api/user_script.dart#L5-L32)).
- It offers `screenOn()`/`screenOff()`, which "Controls real display power … Turning it off requires the device admin permission". It also has `setBrightness(0..1)`, `stopScreensaver()`, `pauseScreensaver(bool)` and `setInteractionActive()` ([docs/js-api.md#L31-L70](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/js-api.md#L31-L70)).
- `getScreensaverSuppressed()` "Always returns true. Because the app fully owns the screen, the web page must stand down its own internal screensaver" ([docs/js-api.md#L70](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/js-api.md#L70)).
- A non-HA page can be the default dashboard as a Custom Page with a full URL ([docs/custom-pages.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/custom-pages.md), "Add a page").

**What waking looks like:** once the panel is powered off, "A panel that **Turn screen off after** powered off takes no touches" ([docs/screensavers.md#L382](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/screensavers.md#L382)). GoCasa's page can't wake it either, because its black Night screen has no camera wake: `motionWake` runs only on the Dim clock (`internal/web/screensaver.templ`, `Night`). So something else has to wake it:
- Kiosk Satellite's own camera motion ("on supported hardware where background camera sessions survive screen off"), the power button, or an HA automation calling `screenOn` ([docs/screensavers.md#L374](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/screensavers.md#L374)). **But** its motion wake is tied to its own screensaver: "The camera runs strictly while the screensaver is on screen" ([docs/screensavers.md#L406](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/screensavers.md#L406)). If GoCasa turns that screensaver off, or holds it off, and calls `screenOff()` itself, camera wake is gone.
- Or GoCasa itself at Night mode's end, but only if the page keeps running with the panel off. *Unverified*; Fully's vendor says WebView scripting stops after a while (`kiosk-dimming-and-motion-options.md` §2).

**For GoCasa:** an optional `if (window.kioskSatellite)` branch, the same shape as the `window.fully` branch and the `display` SSE event proposed in `kiosk-dimming-and-motion-options.md` §2 and §5, so one design serves both:
- **Default: GoCasa's current behaviour, unchanged.** It keeps its own screensaver and black Night page, even inside Kiosk Satellite.
- **Opt-in: let Kiosk Satellite own the night, per display.** This is the only route with a documented camera-wake path: Kiosk Satellite's scheduled screensaver with "Turn screen off after" and "Dismiss on motion". A device setting such as "Kiosk Satellite runs the screensaver" makes GoCasa skip both its idle navigation to `/screensaver` and the Night page it picks server-side (`internal/app/device.go`). An Admin turns it on only after setting up the Kiosk Satellite schedule and checking that it wakes.
  - Don't key this off `getScreensaverSuppressed()`: it returns `true` unconditionally, schedule or not ([screensaver_manager.dart#L1252](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/screensaver/screensaver_manager.dart#L1252)).
- **Page-driven `screenOff()` stays off by default.** Turn it on only once device admin has been verified as provisioned (by hand, outside the page) *and* a wake path has been proven separately.
- **Why the page can't check permission first.** The page bridge exposes no device-admin status and sends `screenOff` with no `prompt: false`. When device admin isn't granted, Kiosk Satellite puts **Android's device-admin grant screen** on the tablet, and the page's `false` arrives only after that prompt ([screen_manager.dart#L356-L375](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/screen/screen_manager.dart#L356-L375), [user_script.dart#L71](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/js_api/user_script.dart#L71)). Its own screensaver timer calls the quiet form instead. An unattended Night mode boundary must never raise that prompt.

Needs an end-to-end test: a GoCasa URL as a Custom Page, cookies sticking, SSE in its WebView, `screenOff()` from a non-HA origin, and what wakes it.

**Caveats:**
- Home Assistant is required (§1).
- Analytics (Basic, Usage, Diagnostics) all default to **on** ([definitions.dart#L12439-L12465](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/settings/definitions.dart#L12439-L12465), [docs/analytics.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/analytics.md)). That clashes with GoCasa's "no telemetry", so docs should say to turn them off.
- It is free only for non-commercial use.

Value: medium, as a shell for HA homes, not a replacement for the Display app.

### 4.3 A bridge contract for the Display app (gocasa-android #11)

Kiosk Satellite's page API is a worked example of the open question "what messages pass between GoCasa's page and the Display app":
- **Presence and identity:** synchronous presence and version detection.
- **Fails soft:** every method returns a promise that never rejects. Failures resolve `null` for queries or `false` for commands, and any transport failure resolves `null` whatever the method ([docs/js-api.md#L23-L30](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/js-api.md#L23-L30), [user_script.dart#L18-L25](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/js_api/user_script.dart#L18-L25)).
- **Signals:** DOM `CustomEvent`s ([docs/js-api.md#L192](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/js-api.md#L192)).
- **State:** one snapshot, `getKioskState()`, plus one event carrying the same object whenever anything changes ([docs/js-api.md#L42-L56](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/js-api.md#L42-L56)).

Adopting that *shape* for a `window.gocasaDisplay` bridge gives GoCasa a bridge it can feature-detect that fails soft, which suits spec principle 2 ("must work with JS partially broken"). Treat a falsy command result as "not done". Design only.

### 4.4 Display app robustness patterns (gocasa-android #3, #8, #13)

**The same core design.** Kiosk Satellite's manifest matches the Display app's decisions: a disabled-by-default `HOME` activity-alias, a camera foreground service and a device-admin receiver ([AndroidManifest.xml#L196-L324](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/AndroidManifest.xml#L196-L324)).

**One difference:**
- gocasa-android #13 puts `showWhenLocked`/`turnScreenOn` on the Wall Activity itself.
- Kiosk Satellite also keeps a separate translucent `WakeActivity` as "the screen-on fallback: launching this lights the panel where the app's wake lock does not" (same range).
- Worth keeping in mind if hardware tests show the Wall Activity's own `turnScreenOn` doesn't re-arm when it's brought back to the front, which #13 lists as unverified.

**Three things it adds:**
- **Crash fuse for the Home role.** The source says "a kiosk that crash-loops while it IS the home app would otherwise strand whoever is in front of the device on a black screen". After 3 chained starts that never draw a frame, it gives Home back to the stock launcher. It does this in native code "because it must work when Dart is exactly what is broken" ([HomeFuse.kt#L12-L33](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/HomeFuse.kt#L12-L33), [#L45](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/HomeFuse.kt#L45)). Cheap insurance for an app that plans to *be* Home (#13).
- **Camera frame watchdog.** No frames for 10 s means a dead session; this covers binds that silently park in `PENDING_OPEN`. Recovery differs by panel state ([CameraMotion.kt#L305](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L305), [#L1442-L1460](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L1442-L1460)):
  - **Lit panel:** rebind after a short backoff.
  - **Dark panel** (the OS suspended the camera): wait for the next screen-on.

  This lines up with VACA's "opens but delivers no frames" note in #8. It also shows that on some devices the camera does *not* survive screen-off, which is the risk #4 flags.
- **Auto-exposure hunt fallback.** Once the slow-exposure mode has been applied, 20 consecutive frames that are lighting-shaped *and* have at most 3 mean-adjusted changed cells (about 10 s) step the exposure ladder and restart the session. Ordinary lighting vetoes don't trigger it ([CameraMotion.kt#L1705](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L1705)). It was added for a Galaxy Tab S6 Lite whose exposure "oscillates forever, which pins a core" ([CameraMotion.kt#L145-L164](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt#L145-L164)).

### 4.5 Display app updates from a folder GoCasa serves

**In Kiosk Satellite:**
- It checks GitHub 20 s after launch and then every 12 h.
- It falls back to the release page when GitHub's 60 requests an hour limit refuses it.
- It picks the APK for the device's architecture.
- A **Custom Repository** is any folder holding GitHub's `releases.json` "saved as is" plus the APKs. Plain `http://` works, and Android's signature check means the folder "can only ever serve a real Kiosk Satellite release".
- Installs are silent where Android allows it:
  - The installer *asks* for `USER_ACTION_NOT_REQUIRED` and handles Android asking for confirmation anyway ([ApkInstaller.kt#L350-L351](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/ApkInstaller.kt#L350-L351)).
  - Its docs say Android 12+ prompts for the first update only, while **Android 11 and older ask every time** unless the device is provisioned as device owner or a helper is installed ([docs/updates.md#L83-L95](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/updates.md#L83-L95)).
  - On Android 12+ the silent path relies on **installer package tracking**: the first in-app update makes the app its own installer of record. A later `adb install` makes `adb` the installer again, so the next update prompts once more ([docs/updates.md#L95](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/updates.md#L95), [#L222](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/updates.md#L222)).
  - Android has its own eligibility rules for silent installs (target SDK, permissions), which I didn't research. These matter for a sideloaded Display app, especially on old tablets and LineageOS builds on old hardware.

([docs/updates.md#L5-L63](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/updates.md#L5-L63), [#L83-L92](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/updates.md#L83-L92)).

**For GoCasa:** this answers gocasa-android #3's "Updates for a sideloaded APK". GoCasa is the one server every Display app can always reach. A folder under `/data` that GoCasa serves statically, with `releases.json` and the APK, means no internet and no GitHub limits. Prefer a folder over embedding the APK in the binary, which would bloat it. Fit: good.

## 5. Low value, or later

- **Sunrise before Wake time (later, in the Display app).** Kiosk Satellite's sunrise alarm starts 10–30 minutes early: "a glow rises from deep red to warm white while the screen brightens from its lowest level to full, then the alarm rings on the last color" ([docs/alarms.md#L67-L71](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/alarms.md#L67-L71), [#L112](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/alarms.md#L112)). GoCasa's issue #112 (closed) has already decided the Member display's Night screen: it's **native**, drawn over the WebView, with "no 'nearly time' cue in v1". A sunrise ramp is therefore a post-v1 native option for the Display app, not a page feature.
- **Devices list with remote settings and profiles.** Kiosk Satellite has a per-device web admin (port 2324: settings, live screenshots, logs) and leader/follower *profiles* that push chosen settings categories to other kiosks ([docs/remote-api.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/remote-api.md), [docs/fleet.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/fleet.md)).
  - GoCasa keeps device settings in cookies only (`internal/app/device.go`: `default_view`, `theme`, `wall_display`, `motion_wake`).
  - The device-identity part is already proposed in `kiosk-dimming-and-motion-options.md` §5. New here are the profiles: one set of settings applied to many screens.
  - gocasa-android #10 kept "no device registry or pairing". Revisit only if #3's "status back to GoCasa's admin pages" is taken up.
- **Postpone the screensaver on motion.** This keeps the camera on between sessions, so the idle timer resets while someone reads ([docs/screensavers.md#L402-L410](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/screensavers.md#L402-L410)). GoCasa runs the camera only on the screensaver and Dim clock. A camera on all day is a privacy cost the household should opt into separately.
- **Pixel shift.** It "Periodically shifts on screen elements every minute to prevent image retention on OLED displays" ([docs/screensavers.md#L22](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/screensavers.md#L22)). GoCasa's Dim clock sits still for hours, and a few pixels of CSS drift is tiny work. It matters only on OLED.
- **Landscape pairs on portrait screens** ([docs/immich.md#L104-L108](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/immich.md#L104-L108)). GoCasa deals one shared deck of single photos and portrait Photo pairs for every Wall display (`docs/adr/0002-photo-rotation-is-its-own-module.md`). A portrait screen shows a pair one photo after the other (`internal/app/rotation.go` `screensaverStep`). Stacking landscape pairs would need a deal per orientation.
- **Slideshow progress bar and tap-edges-to-step** ([docs/immich.md#L29-L33](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/immich.md#L29-L33)). Tap-to-step conflicts with GoCasa's "any touch wakes".
- **Daily forecast in the Week view** ([docs/calendar.md#L62-L64](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/calendar.md#L62-L64)). GoCasa caches today plus 5 days (`internal/app/weather.go`, `forecast_days` 6). A Week view can show any week (`internal/app/calendar.go`), so only days in the cache would get weather, unless the cache grows to 7 or more days.
- **Per-block screensaver schedule.** Each time block gets its own mode, brightness, screen-off timer and per-block *wake overrides* (motion on at night and off by day, for example) ([docs/screensavers.md#L388-L400](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/screensavers.md#L388-L400)).
  - The schedule-owner idea is already in `kiosk-dimming-and-motion-options.md` §5. The new part is per-block wake rules.
  - GoCasa's single Night mode window covers "photos by day, dark at night".
- **Adaptive brightness curve.** Four points against lux "on a log scale, because the eye judges light that way", from the device's sensor or an HA sensor ([docs/screen.md#L22-L48](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/screen.md#L22-L48)). Only a native app can set the backlight, so this is a later Display app feature (#3 lists "brightness control" as unspecified).
- **Secure-context loopback proxy.** Kiosk Satellite serves a plain-http HA from `127.0.0.1:2325` inside the app, so the page becomes a secure context and gets the microphone ([proxy_manager.dart#L10-L30](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/app/lib/managers/proxy/proxy_manager.dart#L10-L30)).
  - In the Display app it would turn on GoCasa's *existing* Wake Lock and in-page camera wake over plain http.
  - But the Display app already chose native keep-awake and a native camera (#4, #8), and the origin change resets the device's cookies (Kiosk Satellite warns of the same "one-time re-login").

## 6. Not a fit

| Kiosk Satellite feature | Why not for GoCasa |
|---|---|
| Voice Satellite, wake words, Voice Match, intercom ([docs/voice-satellite.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/voice-satellite.md), [docs/intercom.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/intercom.md)) | Native audio and ML on the device plus HA Assist; far outside a calendar dashboard and a browser client |
| Music: Sendspin, AirPlay, DLNA, Music Library ([docs/sendspin.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/sendspin.md), [docs/dlna.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/dlna.md)) | Not GoCasa's domain |
| RTSP/ONVIF streaming of the tablet camera ([docs/camera.md#L104](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/camera.md#L104)) | Turns the tablet into a network camera, the opposite of GoCasa's "frames never leave the page" |
| Camera views of other cameras ([docs/cameras.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/cameras.md)) | A home-security feature from HA or Go2RTC; GoCasa has no camera integration and the spec lists none |
| Bluetooth proxy ([docs/esphome.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/esphome.md)) | Relays BLE adverts to HA over ESPHome; native-only and HA-only |
| Face, hand-gesture and person detection ([docs/screensavers.md#L418](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/screensavers.md#L418), [docs/gestures.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/gestures.md)) | Needs on-device ML models or special hardware; motion is enough for wake, and §3.1 makes it better |
| Plugin Manager ([docs/plugins.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/plugins.md)) | Spec §2 non-goal: "Plugin system — integrations are compiled in" |
| "Pause dashboard during screensaver" ([docs/optimizations.md#L20-L54](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/optimizations.md#L20-L54)) | Already true by design: GoCasa's screensaver is its own page, so nothing renders behind it |
| Keep HA connected, filter dashboard updates ([docs/optimizations.md#L14-L18](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/optimizations.md#L14-L18), [#L64](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/optimizations.md#L64)) | HA-frontend specific |
| At a Glance, HA entity states on the screensaver ([docs/at-a-glance.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/at-a-glance.md)) | Needs a live HA connection from the page; GoCasa's screensaver already shows its own agenda and weather |
| Calendar view of HA calendars ([docs/calendar.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/calendar.md)) | GoCasa's calendar already does more: Member lanes, diagonal stripes for shared events (`internal/web/calendar.templ` `eventFill`), Edit scopes |
| Remote admin live screenshots and logs ([docs/remote-api.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/remote-api.md)) | Belong to a native app on the device, not a server-rendered page |
| Analytics on by default ([docs/analytics.md](https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/docs/analytics.md)) | GoCasa promises no telemetry |

## 7. Open questions for whoever designs these

From the last review round. These are design gaps in GoCasa-side proposals, not facts about Kiosk Satellite:

- **Exclude tags, reconnect (§4.1).** A display that was offline can miss the gate closing *and* reopening. On reconnect it should clear its Immich slides, drop pending slide work and fetch a fresh eligible slide, not just check whether the gate is open.
- **Exclude tags, browser cache (§4.1).** Changing cache headers only affects requests that reach GoCasa; a fresh `max-age=86400` copy from before exclusions were on can still be used. Immich image URLs, including the server-rendered first slide and its blurred background, probably need a part tied to the eligibility revision, served `no-store` while exclusions are active.
- **Kiosk Satellite opt-in wording (§4.2).** "Keeps its black Night page" should read "keeps its existing idle behaviour and the Household's configured Night mode (black, dim clock or off)" until an Admin opts that display in.

## 8. Not verified

- That GoCasa's fixed-threshold motion-wake false-wakes on crossfades or misses people in a dark room, and that §3.1 fixes it, on a real tablet.
- That an automatic reload during a GoCasa restart leaves a browser-based Wall display stuck on a 502 page (§3.3 is an inference; Kiosk Satellite's comment describes the same thing in a WebView).
- Whether Immich's metadata search can return face boxes inline (§3.2).
- That Kiosk Satellite works as a GoCasa shell end to end, and what keeps or wakes the page with the panel off (§4.2). Read from its docs and code only; not installed (the brief says not to run it).
- Kiosk Satellite's claims about its own behaviour on devices (silent updates on Android 12+, power numbers in `optimizations.md`) are the vendor's own.

## Sources

Kiosk Satellite at `d78aac9` (`https://github.com/jxlarrea/kiosk-satellite/blob/d78aac9/…`):
- `README.md`, `LICENSE`, `PLUGIN-EXCEPTION.md`
- `docs/`: `screensavers.md`, `immich.md`, `calendar.md`, `js-api.md`, `custom-pages.md`, `updates.md`, `alarms.md`, `screen.md`, `optimizations.md`, `remote-api.md`, `fleet.md`, `analytics.md`, `camera.md`, `cameras.md`, `esphome.md`, `gestures.md`, `plugins.md`, `voice-satellite.md`, `intercom.md`, `sendspin.md`, `dlna.md`, `at-a-glance.md`
- `app/android/app/src/main/kotlin/me/jxl/kiosk_satellite/CameraMotion.kt`, `HomeFuse.kt`; `app/android/app/src/main/AndroidManifest.xml`
- `app/lib/managers/motion/motion_manager.dart`, `app/lib/managers/browser/browser_manager.dart`, `app/lib/ui/kiosk_screen.dart`, `app/lib/managers/screensaver/immich_manager.dart`, `app/lib/ui/photo_frames.dart`, `app/lib/managers/js_api/user_script.dart`, `app/lib/managers/proxy/proxy_manager.dart`, `app/lib/managers/settings/definitions.dart`, `app/lib/ui/setup_screen.dart`

GoCasa (this repo, `90daff5`): `README.md`, `GLOSSARY.md`, `docs/skylite-go-spec.md`, `docs/adr/0002-photo-rotation-is-its-own-module.md`, `internal/web/screensaver.templ`, `internal/web/calendar.templ`, `internal/app/immich.go`, `internal/app/rotation.go`, `internal/app/device.go`, `internal/app/weather.go`, `internal/app/calendar.go`, `internal/app/app.go`, issues #107, #112 and #113.

gocasa-android: issues [#3](https://github.com/DeRusto/gocasa-android/issues/3), [#4](https://github.com/DeRusto/gocasa-android/issues/4), [#8](https://github.com/DeRusto/gocasa-android/issues/8), [#10](https://github.com/DeRusto/gocasa-android/issues/10), [#11](https://github.com/DeRusto/gocasa-android/issues/11), [#13](https://github.com/DeRusto/gocasa-android/issues/13).
