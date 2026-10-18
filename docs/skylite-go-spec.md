# GoCasa — Go Implementation Spec

**Status:** Draft v0.2 — 2026-10-18 (named)
**Name:** **GoCasa** — Go + *casa* (home); binary and repo `gocasa`. A SkyLite-UX clone in function only: no upstream SkyLite-UX code is reused — see §3/§15
**One-line summary:** A from-scratch, self-hosted family calendar dashboard in Go: one static binary, one SQLite file, server-rendered UI, live updates over SSE.

---

## 1. Purpose

Replace the forked SkyLite-UX (Nuxt 4 / Vue / TypeScript / Prisma) with an implementation we own end to end, designed to run untouched on the home server for years while its maintainer is frequently away.

Feature parity target is the *function* of SkyLite-UX as used by this household, not its internal design:

- Household calendar: month / week / day / agenda views, per-person filtering and colors
- Calendar sync: Google Calendar and iCal/webcal feeds in; local events created in-app
- To-do lists with columns, recurring to-dos, sorting/filtering
- Shopping lists
- Chores and rewards (fork feature)
- Weather header
- Photo screensaver (Immich first, Google Photos later if feasible)
- PIN protection and per-user PINs/roles (fork feature)
- Household settings and per-user preferences

## 2. Non-goals (v1)

- Native mobile apps — the web UI must be responsive (phone / tablet / wall display), that is enough
- Public/multi-tenant hosting — single household per instance, like PuppyPad's single-purpose model
- Full CalDAV *server* — we are a CalDAV/Google *client* where write-back is supported (§7)
- Real-time collaborative editing with conflict resolution — last-write-wins, broadcast afterwards
- Email/SMS reminders — in-app display only in v1
- Plugin system — integrations are compiled in behind a common Go interface

## 3. Design principles

1. **One process, one binary, one file.** No Redis, no job server, no Node runtime. Background work is goroutines in the same process. State is one SQLite file.
2. **Server-rendered first.** HTML from the server (`templ`), interactivity from htmx. JavaScript is progressive enhancement (Alpine.js), never the source of truth. The wall display must work with JS partially broken.
3. **Local data is the source of truth for rendering.** Pages never call Google/Immich/weather APIs at request time. Sync writes into SQLite; rendering reads only SQLite. A dead internet connection degrades sync, never the dashboard.
4. **Boring, stdlib-leaning dependencies.** Every dependency below is chosen because it is maintained, widely used, and replaceable. No ORM.
5. **Clean-room.** Behaviour is cloned; upstream AGPL code is not copied (upstream switched MIT → AGPL in 2026.4.0). This keeps our own licensing free — AGPLv3 by choice, as with PuppyPad/PlaylistGen, not by obligation.

## 4. Stack

| Layer | Choice | Why |
|---|---|---|
| Language | Go (current stable) | Small language, fast builds, static binary, official Google client |
| HTTP | stdlib `net/http` with `chi` router (only if stdlib routing gets painful — start stdlib) | Patterns in stdlib mux cover `/events/{id}` style routes |
| Templates | `a-h/templ` | Type-safe, component-like templates that compile to Go; closest productive analogue to Vue SFCs without a JS build |
| Interactivity | htmx + Alpine.js, both vendored and embedded | Partial page swaps, dialogs, drag affordances; no npm toolchain |
| CSS | Tailwind via standalone CLI at build time, output committed/embedded | Utility styling without a Node pipeline in production |
| Database | SQLite via `modernc.org/sqlite` (pure Go, **no CGO**) | `CGO_ENABLED=0` gives a true static binary and painless arm64/amd64 cross-compiles |
| Queries | Hand-written SQL via `sqlc` (generates typed Go from `.sql`) | No ORM magic; SQL is reviewable; compile-time checking like sqlx in Rust |
| Migrations | Embedded SQL migrations run at startup (`golang-migrate` or a ~50-line embedded runner — decide at Phase 0; runner preferred, ponytail) | One reconciling, ordered history — the fork's unioned Prisma histories are the anti-pattern to avoid |
| Google Calendar | Official client `google.golang.org/api/calendar/v3` + `golang.org/x/oauth2/google` | Verified: this is Google's documented Go quickstart path |
| iCalendar | `emersion/go-ical` | RFC 5545 parser/generator; integrates with rrule-go |
| Recurrence | `teambition/rrule-go` v1.8.x | Port of Python dateutil's rrule; used by go-ical itself |
| Live updates | Server-Sent Events (stdlib `http.Flusher`) | One-way server→display push is all a dashboard needs; no WebSocket dependency |
| Secrets at rest | AES-256-GCM with a key from env (`APP_ENCRYPTION_KEY`), stdlib `crypto/aes` + `crypto/cipher` | OAuth tokens/API keys never sit in SQLite as plaintext (the fork stored token columns; we encrypt them) |
| Logging | stdlib `log/slog`, JSON to stdout | Docker/Coolify log capture for free |
| Testing | stdlib `testing` + `net/http/httptest`; golden files for recurrence cases | No test framework dependency |

## 5. Architecture

```
┌──────────────────────── single Go binary ────────────────────────┐
│                                                                  │
│  HTTP handlers (templ pages + htmx partials + /api/*)            │
│        │ reads/writes                                            │
│        ▼                                                         │
│  Store layer (sqlc-generated, SQLite, WAL mode)                  │
│        ▲                                                         │
│        │ writes synced data                                      │
│  Sync engine (one goroutine per integration)                     │
│    ├─ Google Calendar (incremental, syncToken)                   │
│    ├─ iCal / webcal feeds (ETag / Last-Modified)                 │
│    ├─ Open-Meteo weather                                         │
│    └─ Immich (album / people queries for screensaver)            │
│                                                                  │
│  Event bus (in-process chan) ──► SSE hub ──► all browsers        │
│  Scheduler (in-process ticker) ──► due syncs, screensaver data   │
└──────────────────────────────────────────────────────────────────┘
        │ bind-mounted volume
        ▼
  /data/app.db  (+ /data/backups)
```

- **Event bus:** any mutation (local CRUD or a sync that changed rows) publishes `{type, entity, id}`. The SSE hub fans it out; browsers use htmx's SSE extension to swap the affected fragment. This replaces SkyLite's sync-manager/WebSocket layer with ~150 lines.
- **Scheduler:** one ticker goroutine wakes every 30s and dispatches integrations whose `next_sync_at` is due. Per-integration minimum sync interval is stored on the integration row (the fork had this concept; keep it).
- **Failure isolation:** a panicking or erroring integration disables only itself, records `last_error` + backoff, and surfaces in Settings. The dashboard never goes down for an integration.

## 6. Data model (SQLite)

All timestamps stored as UTC epoch seconds **plus**, where relevant, the original timezone ID. All-day dates stored as `YYYY-MM-DD` text, never as timestamps (this is the #1 source of calendar bugs).

| Table | Key columns / notes |
|---|---|
| `users` | `id, name, color, avatar_url, pin_hash (nullable, bcrypt), role (admin/member), created_at` |
| `household_settings` | single row: `household_name, timezone (IANA), week_start (0=Sun), pin_protection_enabled, screensaver_idle_seconds (default 300), screensaver_enabled, night_mode (off/black/dim_clock, default off), night_start, night_end` |
| `user_preferences` | `user_id, key, value` — default view, last filters (replaces localStorage persistence upstream moved to) |
| `integrations` | `id, type (google_calendar / ical / open_meteo / immich / mealie / tandoor), name, config_json, credentials_enc (nullable, AES-GCM), enabled, sync_interval_seconds, last_sync_at, next_sync_at, last_error, sync_cursor (Google syncToken / iCal ETag)` |
| `source_calendars` | `id, integration_id, external_id, name, color_override, enabled, read_only` — one integration exposes many calendars |
| `events` | `id, calendar_id (source_calendars or local), external_id (nullable), title, description, location, start_utc, end_utc, start_tzid, all_day, all_day_date, rrule (nullable text), exdates_json, rdates_json, recurring_master_id (nullable), status, etag/version, updated_at` |
| `event_attendees_users` | `event_id, user_id` — household-member linkage for filtering/colors (events also keep raw attendee text in `events.attendees_json` for external guests) |
| `event_exceptions` | handled as rows in `events` with `recurring_master_id` set + `original_start_utc` — one mechanism for "edit/delete this occurrence only", matching upstream's recurring fix direction |
| `todo_columns` | `id, name, position` |
| `todos` | `id, column_id, title, notes, due_at (nullable), position, completed_at, rrule (nullable), recurring_group_id, created_by` |
| `shopping_lists` | `id, name, integration_id (nullable — Mealie/Tandoor-backed lists), position` |
| `shopping_items` | `id, list_id, name, quantity, note, checked, position, label/food fields preserved raw in `extra_json` for integration round-trips |
| `chores` | `id, name, assigned_user_id, rrule/schedule, points` |
| `chore_completions` | `id, chore_id, user_id, completed_at, points_awarded` |
| `rewards` | `id, name, points_cost` + `reward_redemptions` (`id, reward_id, user_id, redeemed_at`) |
| `photos_cache` | `id, integration_id, external_id, url_or_path, taken_at, people_json` — screensaver rotation source; images proxied through the app, never hot-linked with credentials |

Indexes: `events(start_utc, end_utc)`, `events(calendar_id, external_id)` unique where external_id not null, `todos(column_id, position)`, `shopping_items(list_id, position)`.

## 7. Calendar sync — the risky part, specified first

### 7.1 Rules that apply to every source

1. **Expand on read, store the master.** Recurring masters are stored once with their `rrule`. Occurrences are expanded in Go (rrule-go) only for the requested window, with a hard expansion horizon (e.g. window ± 1 buffer) and a per-series occurrence cap.
2. **Exceptions are rows, not mutations of the master.** An edited/deleted occurrence becomes an exception row keyed by `original_start_utc`; expansion skips originals that have exceptions and merges exception rows in.
3. **Timezone discipline.** Timed events: keep `start_tzid` and render/expand in that zone, store UTC for indexing. Floating/all-day events never pass through a timezone conversion.
4. **Idempotent sync.** Upsert by `(calendar_id, external_id)` inside one transaction per sync run; a crashed sync leaves either the old or the new state, never half.

### 7.2 Google Calendar

- OAuth2 web flow in-app: `/integrations/google/connect` → Google consent → callback stores the token (encrypted). One Google *account* = one integration; its calendars appear under `source_calendars`. (This is the fork/upstream duplication resolved by design: there is exactly **one** Google implementation.)
- Sync: `Events.List` with `SyncToken` for incremental sync after an initial windowed import (−3 months … +12 months of masters; Google returns recurrence masters, expansion is ours). On `410 Gone` (expired sync token), fall back to a full windowed re-import for that calendar.
- Write-back (Phase 2b, not 2a): creating/editing a local event *on a Google calendar* writes through the API and lets the next incremental sync reconcile. Events on read-only or iCal calendars cannot be edited in-app (UI disables it, API enforces it).
- Rate limits: exponential backoff per integration; a 403 quota error sets `last_error` and backs off, it does not retry-loop.

### 7.3 iCal / webcal feeds

- Fetch with stored `ETag`/`If-Modified-Since`; `304` = no-op success.
- Parse with go-ical; upsert by feed UID. Events present in the previous snapshot but absent now are soft-deleted (`status='cancelled'`) for that feed.
- Feeds are read-only in v1.

### 7.4 Local calendars

- A built-in `local` integration with user-created calendars. Full CRUD including recurrence and per-occurrence exceptions — same code path as exception handling for synced series.

## 8. Views and routes

Pages are full `templ` renders; htmx targets swap the inner content region only.

| Route | Purpose |
|---|---|
| `/` | Redirect to default view (calendar month, or per-user preference) |
| `/calendar?view=month\|week\|day\|agenda&date=YYYY-MM-DD&users=1,2` | Main calendar; `date`/`users` deep links are shareable (preserves the fork's `?date=` / `?users=` behaviour) |
| `/calendar/event/new`, `/calendar/event/{id}` | Event dialog (htmx modal): title, calendar, members, all-day, timezone, recurrence editor (preset patterns + custom RRULE string behind "advanced") |
| `/todos` | Columns, drag-to-reorder (POST reorder endpoint), recurring to-dos, sort/filter by due range and status |
| `/lists` | Shopping lists + items, check-off, reorder |
| `/chores` | Chore board, completions, points balances, rewards/redemptions |
| `/screensaver` | Full-screen photo rotation (Immich), clock + today's agenda overlay; layout is separate/minimal like the fork's screensaver layout |
| `/settings` | Users (incl. PIN/role), integrations, household settings, screensaver and Night mode config, PIN lock toggle + "Lock now"; **This device** section (stored in the browser): Wall display switch, camera motion-wake opt-in, wake-lock and camera status (e.g. "needs HTTPS") |
| `/lock` | PIN pad when PIN protection is enabled; session unlock cookie |
| `/api/*` | JSON endpoints for htmx-adjacent clients and any future CLI; HTML handlers remain primary |
| `/events/stream` | SSE endpoint; sends `X-Accel-Buffering: no` and a heartbeat every 25 s so reverse proxies neither buffer nor idle-close it; htmx reconnects on drop |
| `/healthz` | `200` + DB ping — used by Docker/Coolify healthchecks |

**Kiosk behaviour:** pages auto-refresh fragments via SSE and survive a server restart by simply reloading (all state server-side). Idle behaviour applies only to a **Wall display** (a per-browser switch under Settings → This device, Admin PIN needed); other browsers just browse.
- **Idle:** no touch, pointer or key input for `screensaver_idle_seconds` → screensaver. A Wall display holds a Screen Wake Lock (re-acquired on `visibilitychange`) when the browser allows it. Entering the screensaver ends any Admin unlock.
- **Wake:** the first touch only wakes (it isn't passed through to the page underneath) and returns to the page and view that was showing.
- **Night mode** (household setting, off by default) between `night_start` and `night_end`: *Black* releases the wake lock so the device's own power settings may turn the display off, and touch wakes it; *Dim clock* keeps the wake lock and shows a dimmed time and date on black. Touch at night wakes to the dashboard; idle returns to Night mode, not the photo screensaver.
- **Camera motion-wake:** opt-in per Wall display, off by default; needs HTTPS and the browser's camera permission. Runs only while the screensaver or Dim clock shows, never during Black or active use. Frame differencing in the browser at a fixed sensitivity; frames never leave the device; a small camera icon shows while it runs.
- Kiosk boot, auto-login and display power schedules are device setup for the operator, not GoCasa's job.

## 9. Auth and security

- **Threat model:** the trusted home LAN is the perimeter (same model as the rest of the homelab). Remote access, and gating it, is the operator's own layer (§11). The app still ships its own controls; it is not assumed to be LAN-only forever.
- Optional household PIN protection (fork feature): when enabled, a PIN unlocks a long-lived session cookie (`HttpOnly`, `SameSite=Lax`, `Secure` when `BASE_URL` is `https`). Per-user PINs identify *who* is acting for chores/filter defaults — they are convenience identity, not strong auth, and the UI says so.
- Admin role required for Settings mutations (integrations, users, household).
- OAuth tokens, Immich API key, and Mealie/Tandoor credentials: AES-GCM encrypted at rest; the master key comes from env and is never stored in the DB or repo. Losing the key = reconnect integrations (documented, acceptable).
- All forms CSRF-token protected; htmx requests carry the token header.
- No credentials, tokens, or personal values hard-coded in source (placeholder config only — the PuppyPad security.txt lesson).

## 10. Integrations (v1 scope per integration)

| Integration | v1 behaviour |
|---|---|
| Google Calendar | §7.2 — sync in Phase 2a, write-back Phase 2b |
| iCal/webcal | §7.3, read-only |
| Open-Meteo | Current conditions + daily forecast for the header; lat/lon in household settings; no API key needed |
| Immich | API-key auth; album and/or people selection; app proxies thumbnails/originals for the screensaver so no Immich credentials reach the browser |
| Mealie / Tandoor | Shopping-list mirror: pull list, push checked state/add-item. Raw extra fields preserved for round-trip fidelity (the fork's merge flagged this exact risk — test against a real instance before calling it done) |
| Google Photos | **Deferred.** No stable general API path equivalent to Immich's; revisit only if a supported route appears. Screensaver ships Immich-only. |

New integrations implement one interface:

```go
type Integration interface {
    Type() string
    TestConnection(ctx context.Context, cfg Config) error
    Sync(ctx context.Context, store *Store, emit func(Event)) error
}
```

## 11. Configuration and deployment

Config is env-only (12-factor), no config file to drift:

```
ADDR=:8080
BASE_URL=https://casa.example.com   # how clients reach GoCasa; drives Secure cookies, the Google redirect and absolute links
DATA_DIR=/data
TZ=America/Los_Angeles            # household default; users keep IANA zones per event
APP_ENCRYPTION_KEY=<base64 32-byte key>
GOOGLE_CLIENT_ID= / GOOGLE_CLIENT_SECRET=
GOOGLE_REDIRECT_URI=              # optional; defaults to ${BASE_URL}/auth/google/callback, override for the localhost / ssh -L route
SESSION_SECRET=
```

- **Docker:** multi-stage build → final image `scratch` (or `distroless/static`) containing only the binary + CA certs + tzdata. `CGO_ENABLED=0`. Image target: < 30 MB.
- **Compose (dev + Gordon):** one service, one volume (`/data`), `healthcheck` on `/healthz`. Runs under Dockge on Gordon like the rest of the stack; can equally be a Coolify resource later.
- **HTTPS:** GoCasa serves plain HTTP only; the operator's reverse proxy terminates TLS. GoCasa ignores `X-Forwarded-*` headers and takes its own address from `BASE_URL`.
  - *Why:* Screen Wake Lock and webcam motion-wake need a secure context, and Google OAuth needs an `https` redirect on a public-suffix domain. Over plain HTTP everything else works; those features stay off and Settings says HTTPS turns them on.
  - *Recipe:* a name on a domain you own (e.g. `casa.<yourdomain>`), local DNS (router, Pi-hole, AdGuard) pointing it at the host's LAN IP, and a reverse proxy holding a real certificate (e.g. Let's Encrypt via DNS-01). The name needn't resolve publicly: the Google redirect happens in the browser.
  - *Worked example:* Nginx Proxy Manager proxy host → `http://<host>:8080`, Let's Encrypt DNS-01 cert. No custom config: the SSE endpoint handles buffering and idle timeouts itself (§8).
  - *Out of scope:* remote (off-LAN) access. That's the homelab's job (tunnel, VPN, authenticating proxy); GoCasa documents none of them.
- **tzdata is embedded** (`time/tzdata`) so DST rules never depend on the host image.
- **Backup:** SQLite in WAL mode. Nightly job (host cron or in-app scheduler): `VACUUM INTO '/data/backups/app-YYYYMMDD.db'` then prune to last 14. Restore = stop container, swap file, start. Tested restore is part of Phase 0 acceptance, not an afterthought.
- **Upgrade:** replace the binary/image; embedded migrations run at startup, in a transaction, with a pre-migration backup copy taken automatically. Rollback = previous image + pre-migration backup.

## 12. Build phases (each phase ends usable, not just compiling)

### Phase 0 — Skeleton
Binary serves `/healthz` + one templ page; SQLite opens, migrations run; Docker image + compose; backup/restore proven once by hand.
**Accept:** fresh `docker compose up` → page loads; kill + restore from backup → same page.

### Phase 1 — Local calendar core
Users, household settings, local calendars, event CRUD (incl. recurrence + exceptions), month/week/day/agenda views, per-user filters + `?date=`/`?users=` links, SSE live reload.
**Accept:** two browsers open; create/edit/delete (incl. "this occurrence only") in one, the other updates without refresh; DST-boundary and all-day cases in §13 pass.

### Phase 2 — Sync: iCal, then Google
2a: iCal feeds (ETag, soft-delete) → Google incremental sync (syncToken, 410 fallback). Settings UI for integrations, encrypted credentials, per-integration status/errors.
2b: Google write-back.
**Accept:** a real household Google calendar and one public iCal feed stay correct through: a recurring series, one edited occurrence upstream, one deleted occurrence upstream, and a forced full re-sync.

### Phase 3 — Todos and shopping
Todo columns + recurring todos + reorder/sort/filter; shopping lists local-first; Mealie/Tandoor mirror behind the same list UI.
**Accept:** reorder persists across restart; recurring todo completion spawns the next occurrence exactly once; Mealie round-trip preserves item extras.

### Phase 4 — Screensaver, weather, PIN
Immich screensaver, Wall display idle/wake, Night mode, camera motion-wake, Open-Meteo header, household + per-user PIN, lock screen.
**Accept:** an idle Wall display rotates photos with clock/agenda, and a non-Wall-display browser never does; Night mode switches at its hours; motion wakes the screensaver over HTTPS; PIN lock blocks settings; weather renders from cached data with the network cable pulled.

### Phase 5 — Chores/rewards and polish
Chores, points, rewards/redemptions; preferences polish; ICS export of a calendar view.
**Accept:** full household day-in-the-life pass on phone, tablet, and wall display sizes.

## 13. Testing — where clones actually fail

Boundary testing is a release gate, in the house style: test every boundary the code distinguishes, and state which cases were already covered vs newly added.

Mandatory case sets before any phase is "done":

- **Recurrence:** daily/weekly/monthly/yearly, INTERVAL > 1, BYDAY ordinal (2nd Tuesday), COUNT vs UNTIL, EXDATE + RDATE, edited occurrence, deleted occurrence, series that crosses a DST change in both directions
- **Time:** all-day multi-day events, events at 00:00 and 23:59, UTC↔America/Los_Angeles conversions in DST gap (2:30am that doesn't exist) and overlap (1:30am that happens twice), a feed whose events carry a foreign TZID
- **Sync:** first import, no-change sync, upstream edit, upstream delete, expired Google syncToken (410), feed returning 304, feed returning garbage, sync interrupted mid-transaction
- **Limits (test at, below, and above):** title/description length caps, max calendars per integration, max occurrences expanded per series, shopping/todo reorder at first/last position
- **Concurrency:** two clients editing the same event; SSE client disconnect/reconnect catching up

Unit tests cover expansion/timezone logic (pure functions, golden files). HTTP handlers get `httptest` coverage for CRUD + permissions. No e2e browser suite in v1 — manual phase-acceptance passes above instead.

## 14. Risks and open questions

| # | Item | Handling |
|---|---|---|
| 1 | rrule-go edge cases vs Google's own expansion (esp. ordinal BYDAY + DST) | Golden-file tests against known Google outputs in Phase 2; if a class fails, expand Google series from Google's `SingleEvents` listing instead of masters for that calendar (escape hatch, costs one API mode) |
| 2 | Drag-and-drop richness in htmx vs a Vue app | v1: drag for todo/shopping reorder (SortableJS, vendored); calendar event drag is Phase 5 polish, click-to-edit is v1 |
| 3 | Google OAuth verification for a self-hosted app | Personal-use OAuth consent (test-user mode) is sufficient for one household; document the consent-screen steps in the README |
| 4 | Write-back conflicts (event edited in Google and in-app between syncs) | Last-sync-wins with the version/etag recorded; conflict surfaces in Settings log rather than silently overwriting — revisit if it ever fires in practice |
| 5 | Project name | **Resolved 2026-10-18: GoCasa** (binary/repo `gocasa`). Repo creation is still Brandon's step |
| 6 | **Open:** does v1 need CalDAV (Nextcloud/Apple) as a *source* in addition to Google + iCal? | iCal covers read-only Nextcloud export; true CalDAV sync is a Phase 6 candidate using `emersion/go-webdav` if the household needs it |
| 7 | **Open:** keep SkyLite fork PR #80 (upstream merge) unmerged/archived once this spec is accepted? | Spec assumes yes — the fork stays as reference only |

## 15. License

Clean-room implementation of behaviour; no upstream code copied. License is Brandon's choice — AGPLv3 recommended for consistency with PuppyPad and PlaylistGen.
