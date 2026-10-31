# Research: a Home Assistant add-on, and an APK for LineageOS / Chromebook

Researched 2026-10-31 against `main` at `8e36837`. Builds on `docs/research/kiosk-dimming-and-motion-options.md` (Fully Kiosk, Chromebase, HA motion sensors), which is not repeated here.

## Short answer

- **Home Assistant add-on: feasible, small.** GoCasa is already a single static binary that keeps everything in `/data`, which is exactly where an add-on's persistent storage lives. An add-on is a `config.yaml` plus a tiny entrypoint that turns `/data/options.json` into env vars. Ship it **without Ingress** at first (open port 8080, "Open Web UI" button). Ingress needs path-prefix support GoCasa doesn't have yet (see §1.3).
- **HA integration (entities):** mostly not needed. HA's built-in **Remote Calendar** integration can subscribe to GoCasa's existing `/calendar.ics` today. Chore and to-do entities would need a small JSON API and a custom component, which is a separate, larger project.
- **APK, recommended form:** a **thin WebView kiosk shell** (one Activity, about 100 lines of Kotlin) pointed at the GoCasa server. It works on LineageOS and on Chromebooks (as an Android app). It natively keeps the screen on, so the Wall display works **without HTTPS**, which the browser Wake Lock API can't do.
- **APK with the server inside: possible, not recommended.** `CGO_ENABLED=0 GOOS=android GOARCH=arm64` builds cleanly today (verified, 15 MB). But the tablet would become the household server, with Android killing background processes, no Docker, Google connect working only from the tablet itself (over `http://localhost`) unless there's HTTPS, and backups living on the tablet.
- **Chromebook:** the cheapest path is no APK at all. Install GoCasa as a PWA (needs a web manifest plus HTTPS), or use the existing Chromebase kiosk notes. The WebView APK is the fallback when there's no HTTPS.

---

## 1. Home Assistant add-on

### 1.1 What fits already

| Add-on expectation | GoCasa today |
|---|---|
| Persistent data in `/data` | `DATA_DIR=/data` is the default (`internal/config/config.go`) |
| Multi-arch image | `amd64` and `arm64` published. `linux/arm` (armv7, a 32-bit Pi 3) also builds cleanly (verified), just not published |
| Health check | `/healthz`, and `gocasa healthcheck` for the Docker `HEALTHCHECK` |
| Single process, no sidecars | Yes, with SQLite in-process |

### 1.2 What the add-on needs

1. **`config.yaml`** (`name`, `slug`, `version`, `arch: [amd64, aarch64]`, `image: ghcr.io/derusto/gocasa`, `ports: {8080/tcp: 8080}`, `webui: http://[HOST]:[PORT:8080]`, `options`/`schema` for `base_url`, `tz`, and optionally `google_client_id`/`secret`).
2. **Options → env.** The current image is `FROM scratch` with no shell, so either:
   - (a) the add-on uses its own Dockerfile `FROM` the HA base image with a short `run.sh` (`jq` options into env, then `exec /gocasa`), or
   - (b) about 15 lines in `cmd/gocasa`: if `/data/options.json` exists, read it into env before `config.Load`. This keeps one image for both uses.
   Option (b) is smaller and reuses `LoadDotEnv`'s "env already set wins" rule.
3. **Secrets.** `APP_ENCRYPTION_KEY` and `SESSION_SECRET` are required. Asking HA users to run `openssl` is poor UX. Instead, generate both on first start into `/data/secrets.env` and load that file. Caveat: this ties the key to the same backup as the data, so a stolen HA backup decrypts the tokens. That's acceptable for an add-on (HA backups already hold HA's own secrets), but it should be documented.
4. **`BASE_URL` stays a user option** (for example `http://homeassistant.local:8080`). It drives CSRF's trusted origin, cookies and the ICS links, so it can't be guessed reliably.
5. A **repository** (`repository.yaml`, either in this repo or in a `gocasa-ha-addon` repo) so users can add it under Settings → Add-ons → Add-on store → Repositories.

Add-ons only exist on **HA OS / Supervised**. HA Container and Core users already have a path: run the existing Docker image next to HA.

### 1.3 Ingress (sidebar panel, HA login): not yet

HA Ingress proxies the app under `/api/hassio_ingress/<token>/`. GoCasa uses root-absolute paths everywhere: `href="/calendar"`, `src="/static/..."`, `sse-connect="/events/stream"`, about 53 `http.Redirect` calls, and cookies with `Path: "/"`. A `<base>` tag doesn't rewrite root-absolute URLs. Ingress support would therefore mean prefixing every URL with a request-scoped base path (taken from `X-Ingress-Path`) across all templates and redirects. That's a mechanical but broad change. CSRF would also need the HA origin as a trusted origin.

Workaround without Ingress: a **Webpage** dashboard card or panel (an iframe) pointing at `http://<ha-host>:8080`. GoCasa sets no `X-Frame-Options` or CSP, so framing works. **But** if HA is served over HTTPS, the browser blocks an `http://` iframe as mixed content, so GoCasa needs HTTPS too in that setup. Even then, if HA and GoCasa are on different *sites* (for example two IP addresses, or unrelated hostnames), GoCasa's cookies are `SameSite=Lax` and are left out of the cross-site frame. Admin unlock and per-device settings then won't stick, and Chrome's third-party cookie blocking applies on top. Use same-site names (for example `ha.home.example` and `casa.home.example`) or open GoCasa directly.

### 1.4 Data into HA without an integration

- **Calendar:** add HA's **Remote Calendar** integration with `<BASE_URL>/calendar.ics`. No code needed.
- **Chores, to-dos, points:** there's no JSON API today. A small read-only `/api/summary` JSON endpoint plus HA's `rest` sensor would give automations ("everyone's chores are done") without writing a custom component. Only build this if someone asks for it.

## 2. APK for LineageOS

### 2.1 Option A: WebView kiosk shell (recommended)

A single Activity hosting a full-screen `WebView` on the server URL, entered on first launch.

| Concern | How the shell handles it |
|---|---|
| Keep the screen awake | `FLAG_KEEP_SCREEN_ON` natively. **No HTTPS needed**, unlike the Wake Lock API GoCasa uses today |
| Full screen / kiosk | Immersive mode. Optionally Android screen pinning (`startLockTask`), no MDM needed |
| Start on boot | `BOOT_COMPLETED` receiver, or set it as the home launcher on a dedicated tablet |
| Cookies (device settings, admin unlock) | `CookieManager` persists them, so per-device settings survive restarts |
| Live updates (SSE) | Work in WebView as in Chrome |
| Camera motion-wake | Still needs a secure context, since WebView treats `http://192.168.x.x` as insecure too. Either use HTTPS or later do motion detection natively (CameraX) |
| Screen off/on at night | Possible natively (`WindowManager.LayoutParams.screenBrightness`, or a device-admin `lockNow`). See the kiosk research doc; Fully Kiosk already does all of this for €9 |
| LineageOS specifics | LineageOS ships Chromium WebView and has no Google Play Services. The shell needs neither, so sideload it or publish it on F-Droid |

Build: Gradle with a debug key, or a GitHub Actions job producing a signed APK per release. With no Play Store and no Play Services, it's a little over 1 MB.

The honest comparison: **Fully Kiosk Browser** already does this and more (REST control, motion, brightness, HA integration). A custom shell only makes sense to avoid the €9/device licence or to integrate tightly (for example GoCasa's night-mode schedule driving real display-off via a JS bridge).

### 2.2 Option B: server in the APK (feasible, not recommended)

- **Builds:** `CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build ./cmd/gocasa` succeeds today, because modernc SQLite is pure Go. It is not run-tested on a device.
- **Running it:** Android 10+ forbids executing files from app data. The binary has to be packed as `jniLibs/arm64-v8a/libgocasa.so` and executed from `nativeLibraryDir` (the trick Termux plugins use), then kept alive by a foreground service with a persistent notification. Config would be written to the app's files dir as `.env`.
- **Risks:** the pure-Go DNS resolver reads `/etc/resolv.conf`, which Android doesn't have, so Google, Immich, Mealie and weather lookups may fail without a workaround (needs device testing). Doze and OEM battery killers (less of a problem on LineageOS) can stop the service. Google OAuth needs an HTTPS redirect, except that GoCasa already supports `GOOGLE_REDIRECT_URI=http://localhost:8080/...` (see README). That works here because the server is on the tablet, as long as you connect from the tablet's own browser. Nightly backups would sit on the tablet's flash. Other screens would need the tablet's IP.
- This only fits a **single-screen, no-homelab** household. That's a different product from "self-hosted on your homelab".

## 3. Chromebook / Chromebase

In order of preference:

1. **PWA.** Add `manifest.webmanifest` (name, icons, `display: fullscreen`, `start_url: /`) and a `<link rel="manifest">`. Chrome then offers "Install", which opens a standalone window with no browser chrome. Installing needs HTTPS. Over plain HTTP, "Create shortcut → Open as window" gives most of the effect. About 15 lines and one icon. This is also the best path on Android tablets running Chrome.
2. **WebView APK from §2.1**, via ChromeOS's Android support (ARCVM). It works, and `FLAG_KEEP_SCREEN_ON` is honoured, which solves the no-HTTPS wake problem on ChromeOS too.
3. **Linux (Crostini).** The `linux/amd64` or `arm64` binary runs there unchanged, but Crostini's port forwarding and suspend behaviour make it a poor always-on server.
4. Managed **kiosk**: covered in the kiosk research doc (needs a licence).

## Suggested order of work

| Step | Size | Value |
|---|---|---|
| Document "use Remote Calendar with `/calendar.ics`" in the README | Docs only | HA users get the calendar today |
| PWA manifest + icon | About 15 lines | Install on Chromebook and Android without an APK |
| HA add-on: `options.json` loader, first-run secret generation, `config.yaml`, repo | About 60 lines + yaml | One-click install on HA OS |
| WebView kiosk APK + CI build | About 150 lines Kotlin + Gradle | Wake without HTTPS, kiosk on LineageOS and Chromebook |
| Base-path support for HA Ingress | Touches every template and redirect | Sidebar panel with HA auth |
| JSON summary endpoint for HA sensors | Small, on demand | Chore and to-do automations |
| Server-in-APK | Large, with ongoing device testing | Niche; skip unless asked |

### Not verified

- Running the `android/arm64` binary on a real device (DNS, CA store at `/system/etc/security/cacerts`).
- HA Remote Calendar's handling of GoCasa's recurring events and time zones (the ICS is standard RFC 5545, so it should work).
- WebView `FLAG_KEEP_SCREEN_ON` behaviour under ChromeOS ARCVM on the Chromebase.
