# GoCasa — Go Implementation Spec

**Status:** v1.0 — 2026-10-17. Every decision on the [GoCasa v1 spec map](https://github.com/DeRusto/goCasa/issues/1) is folded in; ready for `/to-spec` → `/to-tickets`. Vocabulary follows `GLOSSARY.md`.
**Name:** **GoCasa** — Go + *casa* (home); binary and repo `gocasa`. A SkyLite-UX clone in function only: no upstream SkyLite-UX code is reused — see §3/§15
**One-line summary:** A from-scratch, self-hosted family calendar dashboard in Go: one static binary, one SQLite file, server-rendered UI, live updates over SSE.

---

## 1. Purpose

Replace the forked SkyLite-UX (Nuxt 4 / Vue / TypeScript / Prisma) with an implementation we own end to end, designed to run untouched on the home server for years while its maintainer is frequently away. GoCasa starts empty: no data is imported from the fork; synced calendars repopulate and the rest is re-entered.

Feature parity target is the *function* of SkyLite-UX as used by this household, not its internal design:

- Household calendar: month / week / day / agenda views, Member filtering and colours
- Calendar sync: Google Calendar (read and write-back) and iCal/webcal feeds in; local events created in-app
- To-do lists with columns, Recurring to-dos, sorting/filtering
- Shopping lists, local or Mirrored from Mealie
- Chores, points, Rewards and Redemptions
- Today page (home): agenda for today and tomorrow, today's Chores, to-dos due today or overdue, large weather header
- Weather: large header on Today, slim pill on every other page
- Photo screensaver (Immich, with a Fallback folder) on a Wall display, with Night mode and optional camera motion-wake
- Admin PINs and optional Locks
- Household settings and per-device preferences (theme, Default view); no per-Member preferences

GoCasa is general-purpose: it targets any device with a modern browser, never one specific device. Device features (wake lock, camera) are progressive enhancement.

## 2. Non-goals (v1)

- Native mobile apps — the web UI must be responsive (phone / tablet / wall display), that is enough
- Public/multi-tenant hosting — single Household per instance, like PuppyPad's single-purpose model
- Full CalDAV *server*, and CalDAV as a calendar *source* — iCal export covers read-only Nextcloud/Apple; true CalDAV sync is a Phase 6 candidate (`emersion/go-webdav`)
- Real-time collaborative editing — local calendars are last-write-wins, broadcast afterwards; Google calendars refuse stale writes (§7.2)
- Email/SMS reminders — in-app display only in v1
- Plugin system — integrations are compiled in behind a common Go interface
- Translation — English only; dates and times follow the Household's 12/24-hour and week-start settings
- Remote (off-LAN) access and device kiosk setup — the operator's job (§11)
- Tandoor, Google Photos, online image services (Unsplash and the like), rotating Chores, richer Mealie items (foods/units/labels) — future options after v1

## 3. Design principles

1. **One process, one binary, one file.** No Redis, no job server, no Node runtime. Background work is goroutines in the same process. State is one SQLite file.
2. **Server-rendered first.** HTML from the server (`templ`), interactivity from htmx. JavaScript is progressive enhancement (Alpine.js), never the source of truth. The wall display must work with JS partially broken.
3. **Local data is the source of truth for rendering.** Pages never call Google/Immich/Mealie/weather APIs at request time. Sync writes into SQLite; rendering reads only SQLite. A dead internet connection degrades sync, never the dashboard.
4. **Upstream wins for mirrored data.** Google calendars and Mealie lists are mirrors: GoCasa writes upstream first and changes its copy only from what comes back. There is no offline write queue.
5. **Boring, stdlib-leaning dependencies.** Every dependency below is chosen because it is maintained, widely used, and replaceable. No ORM.
6. **Clean-room.** Behaviour is cloned; upstream AGPL code is not copied (upstream switched MIT → AGPL in 2026.4.0). This keeps our own licensing free — AGPLv3 by choice, as with PuppyPad/PlaylistGen, not by obligation.

## 4. Stack

| Layer | Choice | Why |
|---|---|---|
| Language | Go (current stable) | Small language, fast builds, static binary, official Google client |
| HTTP | stdlib `net/http.ServeMux` only (Go 1.22+ method and wildcard patterns); middleware are plain `func(http.Handler) http.Handler` wrappers | No router dependency; moving to chi would be a new decision, not a default |
| Templates | `a-h/templ` | Type-safe, component-like templates that compile to Go; closest productive analogue to Vue SFCs without a JS build |
| Interactivity | htmx and its SSE extension, vendored at pinned versions and embedded (served from `/static/`); Alpine.js is vendored the same way when a feature first needs it | Partial page swaps, dialogs, drag affordances; no npm toolchain |
| CSS | Tailwind via standalone CLI at build time, output committed/embedded | Utility styling without a Node pipeline in production |
| Database | SQLite via `modernc.org/sqlite` (pure Go, **no CGO**) | `CGO_ENABLED=0` gives a true static binary and painless arm64/amd64 cross-compiles |
| Queries | Hand-written SQL via `sqlc` (generates typed Go from `.sql`) | No ORM magic; SQL is reviewable; compile-time checking like sqlx in Rust |
| Migrations | Our own embedded forward-only runner: numbered `.sql` files in `embed.FS`, one transaction each, version in `PRAGMA user_version` (no migrations table, no down files). Refuses to start on a DB whose `user_version` is newer than the binary | One reconciling, ordered history — the fork's unioned Prisma histories are the anti-pattern to avoid |
| Google Calendar | Official client `google.golang.org/api/calendar/v3` + `golang.org/x/oauth2/google` | Google's documented Go path |
| iCalendar | `emersion/go-ical` | RFC 5545 parser/generator; integrates with rrule-go |
| Recurrence | `teambition/rrule-go` v1.8.x — **local calendars only** | Port of dateutil's rrule. Correct if DTSTART is built in the event's zone; needs our RFC 5545 DST fixes (§7.1). Unmaintained since 2023: vendor or fork if we hit a bug |
| Live updates | Server-Sent Events (stdlib `http.Flusher`) | One-way server→display push is all a dashboard needs; no WebSocket dependency |
| Secrets at rest | AES-256-GCM with a key from env (`APP_ENCRYPTION_KEY`), stdlib `crypto/aes` + `crypto/cipher` | OAuth tokens/API keys never sit in SQLite as plaintext |
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
│    ├─ Google Calendar (singleEvents + syncToken, write-back)     │
│    ├─ iCal / webcal feeds (ETag / Last-Modified)                 │
│    ├─ Mealie shopping lists (poll + full diff, write-back)       │
│    ├─ Open-Meteo weather                                         │
│    └─ Immich + Fallback folder (screensaver photo lists)         │
│                                                                  │
│  Event bus (in-process chan) ──► SSE hub ──► all browsers        │
│  Scheduler (in-process ticker) ──► due syncs, nightly backup     │
└──────────────────────────────────────────────────────────────────┘
        │ bind-mounted volume
        ▼
  /data/app.db  (+ /data/backups, /data/photo-cache, /data/photos)
```

- **Event bus:** a committed change publishes its **kind**: one of `event` (events and calendars), `todo`, `list`, `chore`, `points`, `member`, `household` (household settings, Locks, the Fallback folder) or `integration` (an integration's health, settings and picks, and the weather). The kind is the SSE event name and its data; there is no entity or id. A change declares its kinds to the transaction helper, which sends each once, only after commit. A sync publishes only when its rows actually changed (an iCal feed answering 200 with the same events publishes nothing), and a failing sync only when its health changes (to Failing, to Down, or recovered); the exception is the weather, whose every fetch publishes `integration`. Each page refetches its `#live` region for the kinds it lists. This replaces SkyLite's sync-manager/WebSocket layer with ~150 lines.
- **Scheduler:** one ticker goroutine wakes every 30s and dispatches integrations whose `next_sync_at` is due, plus the nightly backup (§11). Per-integration sync interval is stored on the integration row.
- **Failure isolation:** a panicking or erroring integration disables only itself, records `last_error` + backoff, and surfaces in Settings. The dashboard never goes down for an integration.

## 6. Data model (SQLite)

All timestamps stored as UTC epoch seconds **plus**, where relevant, the original timezone ID. All-day dates stored as `YYYY-MM-DD` text, never as timestamps (this is the #1 source of calendar bugs). Members, Chores and Rewards are archived, never hard-deleted while they have history.

| Table | Key columns / notes |
|---|---|
| `members` | `id, name, color, avatar (nullable BLOB: an uploaded picture downscaled to 256 px, stored in the DB so the backup covers it; else initials on colour), role (admin/member), pin_hash (bcrypt; required for Admins, not allowed otherwise), pin_failures, pin_blocked_until, archived, created_at`. At least one active Admin always: refuse to archive, delete or demote the last one. Members are people or pets |
| `household_settings` | single row: `household_name, timezone (IANA), week_start (0=Sun), clock_24h, units (metric/imperial), weather_lat, weather_lon, default_view (default 'today'), lock_calendar, lock_chores, lock_todos (all default false), screensaver_enabled, screensaver_idle_seconds (default 300), seconds_per_photo (default 30), fallback_folder_enabled, night_mode (off/black/dim_clock, default off), night_start, night_end, photo_fit (fill/blur, default fill), photo_captions, photo_zoom (both default true)`. `default_view` applies to devices that haven't set their own |
| `integrations` | `id, type (local / google_calendar / ical / open_meteo / immich / mealie), name, config_json, credentials_enc (nullable, AES-GCM), account_sub (Google OpenID `sub`, nullable), status (ok / reconnect_needed), enabled, sync_interval_seconds, last_sync_at, last_success_at, consecutive_failures, next_sync_at, last_error, sync_cursor (Google: each calendar's syncToken and the next daily re-import, as JSON / iCal ETag)`. One Google account = one integration; one Mealie integration per instance |
| `source_calendars` | `id, integration_id, external_id, name, color, enabled, read_only` — one integration exposes many calendars |
| `source_calendar_members` | `calendar_id, member_id` — default Members for events synced from that calendar |
| `events` | `id, calendar_id, external_id (nullable; Google instance id / iCal UID), title, description, location, start_utc, end_utc, start_tzid, all_day, all_day_date, rrule (nullable; local Series only), exdates_json, rdates_json, recurring_master_id (nullable), original_start_utc (nullable), status, etag, external_series_id (Google `recurringEventId`), local_override, updated_at`. Google occurrences are plain rows (Google expands them); local Series are masters plus One-off edit rows. Title/time are nullable so a cancelled One-off edit row can exist |
| `event_members` | `event_id, member_id` — an event with no rows is a **Household event**. Synced events take their calendar's defaults; `events.local_override` marks a Member change made in GoCasa (on the event, so a change to a Household event holds too), which survives syncs and is never written back |
| `todo_columns` | `id, name, position` |
| `todos` | `id, column_id, title, notes, due_date (required when repeating), position, completed_at, repeat_rule (nullable; preset only), repeat_from_completion (0 = from schedule, the default; 1 = from completion), next_id (nullable: the copy this one's completion created), created_at`. Copies link into a chain through next_id. One open copy per chain: a copy with a next copy can't be deleted or re-opened while that one is completed |
| `shopping_lists` | `id, name, integration_id (nullable — Mealie-backed), external_id, hidden, position` |
| `shopping_items` | `id, list_id, external_id (Mealie item id; the row's identity for Mirrored lists), note/display, checked, position, extra_json (the full Mealie `ShoppingListItemOut`, sent back whole on every write)` |
| `chores` | `id, name, schedule (preset: daily / chosen weekdays / weekly / monthly), kind (each/anyone), points, archived` + `chore_assignees` (`chore_id, member_id`; people only) |
| `chore_completions` | `id, chore_id, member_id, round_start (date), completed_at, points_awarded` — unique on `(chore_id, member_id, round_start)`; for an Anyone chore, one per `(chore_id, round_start)` |
| `rewards` | `id, name, points_cost, archived` |
| `redemptions` | `id, reward_id, member_id, requested_at, status (pending/approved/declined), decided_at, decided_by` |
| `point_adjustments` | `id, member_id, points (±), reason, created_at, created_by` |
| `photos_cache` | `id, source (immich/folder), external_id_or_path, taken_at (local time taken, as UTC), people_json, cache_path (nullable), cache_bytes, last_used_at, width, height (upright; 0 unknown), place` — screensaver rotation source; replaced on each refresh; images always proxied through the app |

Indexes: `events(start_utc, end_utc)`, `events(calendar_id, external_id)` unique where external_id not null, `events(recurring_master_id, original_start_utc)` unique, `todos(column_id, position)`, `shopping_items(list_id, position)`, `shopping_items(list_id, external_id)` unique where not null.

A Member's balance = sum of `chore_completions.points_awarded` − approved Redemptions ± Adjustments.

## 7. Calendar sync — the risky part, specified first

### 7.1 Rules that apply to every source

1. **Local Series: expand on read, store the master.** Local recurring masters are stored once with their `rrule` and expanded with rrule-go only for the requested window, with a hard expansion horizon and a per-series occurrence cap. Google calendars store Google's own occurrences instead (§7.2).
2. **One-off edits are rows, not mutations of the master.** An edited or deleted Occurrence becomes a row keyed by `(recurring_master_id, original_start_utc)`; expansion skips originals that have rows and merges the rows in. EXDATEs and cancelled rows are both honoured.
3. **Timezone discipline.** Timed events: keep `start_tzid`; build DTSTART with `time.LoadLocation(start_tzid)` (in UTC, DST is silently ignored); store UTC for indexing. Floating/all-day events never pass through a timezone conversion. RFC 5545 DST rules on top of rrule-go: an Occurrence whose local time doesn't exist (spring-forward gap) is **skipped**; an ambiguous time (fall-back) takes the **first** offset.
4. **Monthly and yearly presets clamp.** A monthly or yearly preset Series made in GoCasa (local or Google) on a day some months lack falls on those months' last day instead of skipping them as RFC 5545 does. The anchor is in the RRULE itself, so it stays standard: the 29th adds `BYMONTHDAY=28,29;BYSETPOS=-1`, the 30th `BYMONTHDAY=28,29,30;BYSETPOS=-1`, the 31st `BYMONTHDAY=28,29,30,31;BYSETPOS=-1`, yearly on February 29 `BYMONTH=2;BYMONTHDAY=28,29;BYSETPOS=-1`. Custom rules and Series made in Google's own UI keep their rule (and skip).
5. **Idempotent sync.** Upsert by `(calendar_id, external_id)` inside one transaction per sync run; a crashed sync leaves either the old or the new state, never half.
6. **Members.** A synced event takes its calendar's default Members; a Member change made in GoCasa sets `local_override` and survives later syncs (an upstream title or time edit doesn't reset it). It resets only if the event is deleted upstream and returns as a new event. No attendee-email matching in v1. Colour: the Member's colour (every Member's if several); a Household event uses its calendar's colour.

### 7.2 Google Calendar

**Source of truth:** Google wins. GoCasa never shows a version of a Google event that Google doesn't have; Member assignments are the only GoCasa-only data (following the edit scope).

- **Connect:** OAuth2 web flow in-app: `/integrations/google/connect` → consent → callback stores the token encrypted. Scopes from day one: `calendar.events` + calendar-list read + `openid email` (if `calendar.events` doesn't cover `CalendarList.list`, add `calendar.calendarlist.readonly`, else `calendar.readonly`). A **Google account** is identified by its OpenID `sub`; its calendars appear under `source_calendars`. Exactly **one** Google implementation.
- **Sync:** `Events.list` with `singleEvents=true` and `syncToken`. Store occurrences from 3 months back to 12 months ahead; drop anything outside that on each sync. A cancelled instance removes its row. Beyond 12 months, Google events aren't shown, with a notice. A **daily full re-import** at a random time moves the window, stores a fresh sync token and repairs anything incremental sync missed. On `410 Gone`, full re-import for that calendar.
- **Freshness:** sync every 2 minutes, straight after every write, and on a "Sync now" button. No push notifications (they'd need the server exposed to the internet).
- **Writes (Phase 2b):** a GoCasa edit goes to Google first; the event shows "Saving…" and the local copy changes only through the sync that runs straight after. If Google can't be reached the edit fails and nothing changes. Every write sends `If-Match` with the etag GoCasa last saw; on `412 conditionNotMet` GoCasa reloads the event, shows the current version, and the person re-applies their change. iCal and read-only calendars cannot be edited (UI disables it, API enforces it).
- **Edit scopes on Google:** This event → that occurrence; All events → the whole series; This and following → GoCasa performs the Split: create the new series, then end the old one with UNTIL (both with `If-Match`); if ending fails, delete the new series and show an error. Edits made in Google itself are shown as synced.
- **Token failure:** `invalid_grant` puts the Google account into **Reconnect needed**: syncing stops, Settings and the calendar page's stale-sync notice say so, and its events stay visible as last known. Transient failures (network, 5xx, 429) retry on the normal schedule and never cause Reconnect needed. A 403 quota error sets `last_error` and backs off exponentially.
- **Reconnect** overwrites that account's token and keeps its calendars, Member defaults and sync cursors; consenting as a different login is refused ("that's a different account; add it as a new connection").
- **Disconnect** calls Google's revoke endpoint, deletes the token, and deletes its calendars and events. The confirm dialog warns that calendar→Member defaults and per-event Member overrides are lost.
- **Stale-sync notice:** after 3 missed syncs, or when Reconnect needed, the calendar page names the calendar and when it last updated; Settings shows details.

### 7.3 iCal / webcal feeds

- Fetch with stored `ETag`/`If-Modified-Since`; `304` = no-op success.
- Parse with go-ical; upsert by feed UID. Events present in the previous snapshot but absent now are soft-deleted (`status='cancelled'`) for that feed.
- Feeds are read-only in v1.

### 7.4 Local calendars

- A built-in `local` integration with user-created calendars. Full CRUD including recurrence and One-off edits.
- **Edit scopes** for editing or deleting an Occurrence of a Series (local and Google): **This event / This and following events / All events**.
  - **All events** is for corrections only: title, location, description and Members, across the whole Series, past included; earlier One-off edits keep their own changes. It isn't offered for time, duration or repeat-rule changes.
  - Time and repeat-rule changes use **This event** or **This and following**, defaulting to the selected Occurrence, so the past is never rewritten. On a Series that hasn't started yet, This and following on its first Occurrence covers everything.
  - **Split:** This and following ends the Series just before the chosen Occurrence and starts a new, independent Series from it. One-off edits after the Split point are reset, after a warning that gives the count. The halves aren't linked afterwards. Deleting with This and following just ends the Series.

## 8. Views and routes

Pages are full `templ` renders; htmx targets swap the inner content region only. Every page except Today carries a slim weather pill (current temperature + today's high/low).

| Route | Purpose |
|---|---|
| `/` | Redirect to this device's Default view (stored in the browser), else the Household `default_view`. Any main page (Today, calendar view + Member filter, To-dos, Lists, Chores) can be made the device default via "Set as default view on this device" in its view menu (no Lock); a calendar default always opens on today. `?date=`/`?users=` links never change it |
| `/today` | **Today** page, for everyone (no Member filter). Large header: clock, date, current conditions, today's high/low, 5-day forecast. Agenda for today and tomorrow (today's past events greyed); today's Chores (tap to complete by avatar); to-dos due today plus overdue ones, marked overdue. No shopping lists. Default `default_view` for a new Household |
| `/calendar?view=month\|week\|day\|agenda&date=YYYY-MM-DD&users=1,2` | Main calendar; `date`/`users` deep links are shareable (preserves the fork's behaviour). Household events show whatever the filter. Stale-sync notice and "Sync now" (§7.2) |
| `/calendar/event/new`, `/calendar/event/{id}` | Event dialog (htmx modal): title, calendar, Members, all-day, timezone, recurrence editor (preset patterns + custom RRULE string behind "advanced"); edit-scope picker that greys out scopes that don't apply (§7.4); "Saving…" for Google events |
| `/todos` | Columns, drag-to-reorder (POST reorder endpoint), Recurring to-dos, sort/filter by due range and status |
| `/lists` | Shopping lists + items, check-off, reorder (local lists); Mirrored lists per §10 |
| `/chores` | Chore board (Late Rounds marked, Missed ones gone), completions by avatar tap, points balances, Rewards, pending Redemptions |
| `/screensaver` | Full-screen photo rotation (Immich, else Fallback folder), clock + today's agenda overlay. With nothing to show: clock and agenda on a plain dark background |
| `/settings` | Always behind an Admin PIN (tap your Admin avatar, enter your PIN). Members (avatar upload, role, PIN, archive), Locks, household settings (incl. weather, units, clock, default view, Night mode, screensaver idle and seconds per photo), integrations (Google accounts incl. Reconnect needed, iCal feeds, Immich album/person pickers + "Refresh now", Fallback folder toggle + image count, Mealie with per-list hide), "Lock now". **This device** section (stored in the browser): Wall display switch, camera motion-wake opt-in, theme (Light/Dark/Auto, default Auto), current Default view + "Clear", wake-lock and camera status (e.g. "needs HTTPS") |
| `/lock` | Admin PIN pad + "Lock now" |
| `/api/*` | JSON endpoints for htmx-adjacent clients and any future CLI; HTML handlers remain primary |
| `/events/stream` | SSE endpoint; sends `X-Accel-Buffering: no` and a heartbeat every 25 s so reverse proxies neither buffer nor idle-close it; htmx reconnects on drop |
| `/healthz` | `200` + DB ping — used by Docker/Coolify healthchecks |

**CLI:** `gocasa reset-pin <member>` — a one-off command in the container for when the last Admin forgets their PIN.

**Recurring to-dos:** a chain with one open copy at a time; completing it creates the next copy (never pre-generated).
- **Repeat from schedule** (default): next due = first rule date strictly after both the completed copy's due date and today (early completion doesn't consume the next slot; missed dates are skipped, not stacked). **Repeat from completion:** next due = completion date + interval.
- Monthly and yearly repeats clamp to the month's last day without drifting, with the same RRULE anchor as calendar presets (§7.1): due on the 31st goes Jan 31 → Feb 28/29 → Mar 31 → Apr 30. Repeat from schedule anchors to the due day, Repeat from completion to the completion day. A chain whose rule lacks the anchor gains it at its next completion, from its due day.
- Rules are the presets only (daily / weekly on days / monthly / yearly, every N); no custom RRULE, no COUNT/UNTIL. A chain ends by turning repeat off or "delete all future". A due date is required to repeat (defaults to the first matching date from today).
- Un-completing deletes the next copy if it's still open; if it's already completed, refuse ("re-open the later one first").
- Edits carry forward: the next copy is cloned from the completed one; completed copies are history. A rule change takes effect at the next completion. No edit scopes for to-dos.

**Chores:** a Chore comes back on a schedule; each Round is done or lapses, and nothing is ever owed afterwards (unlike a to-do).
- Recurring on presets only, fixed points; no one-off Chores. Assignees are people (pets can't be assigned). **Each chore** (default): every assignee does their own and earns the points. **Anyone chore:** the first assignee to do it completes the Round.
- Once per Round per Member. Completing is immediate, credited to whoever taps their avatar (no PIN, never remembered on screen); the chore Lock is the only gate. An Admin can undo a completion, which removes its points.
- A Round that ended undone is **Late** and can still be claimed for full points until the next Round ends; then it's **Missed** and leaves the board (an Anyone chore can be claimed late only if nobody did that Round). Older fixes go through an Adjustment.
- A Redemption is requested by a Member and spends nothing until an Admin approves (PIN) or declines; it can't take a balance below zero. Only an Admin Adjustment (with a reason) can. Rewards have no stock limits. Each completion stores its points, so changes only affect future Rounds. Chores never appear on the calendar.

**Kiosk behaviour:** pages auto-refresh fragments via SSE and survive a server restart by simply reloading (all state server-side). Idle behaviour applies only to a **Wall display** (a per-browser switch under Settings → This device, Admin PIN needed); other browsers just browse.
- **Idle:** no touch, pointer or key input for `screensaver_idle_seconds` → screensaver. A Wall display holds a Screen Wake Lock (re-acquired on `visibilitychange`) when the browser allows it. Entering the screensaver ends any Admin unlock.
- **Wake:** the first touch only wakes (it isn't passed through to the page underneath) and returns to the device's Default view at today.
- **Night mode** (household setting, off by default) between `night_start` and `night_end`: *Black* releases the wake lock so the device's own power settings may turn the display off, and touch wakes it; *Dim clock* keeps the wake lock and shows a dimmed time and date on black. Touch at night wakes to the dashboard; idle returns to Night mode, not the photo screensaver.
- **Camera motion-wake:** opt-in per Wall display, off by default; needs HTTPS and the browser's camera permission. Runs only while the screensaver or Dim clock shows, never during Black or active use. Frame differencing in the browser at a fixed sensitivity; frames never leave the device; a small camera icon shows while it runs.
- Kiosk boot, auto-login and display power schedules are device setup for the operator, not GoCasa's job.

## 9. Auth and security

- **Threat model:** the PIN stops kids and visitors, not attackers. The trusted home LAN is the perimeter (same model as the rest of the homelab). Remote access, and gating it, is the operator's own layer (§11). The app still ships its own controls; it is not assumed to be LAN-only forever.
- **Admin PINs only.** Every Admin must have a PIN; a Member who isn't an Admin can't have one. An Admin's PIN unlocks everything; there are no trust levels or PIN scopes. There is no household PIN.
- **Settings is always locked.** Three optional **Locks**, each off by default and changed only by an Admin: changing the calendar (add/edit/delete events), completing chores, completing to-dos. Shopping lists are never locked. Viewing is never locked.
- **Unlock lifetime:** one PIN entry unlocks Settings and every Lock on that device until 5 minutes pass with no activity, someone taps "Lock now", or a Wall display enters the screensaver. No remember-device option. The unlock is a session cookie (`HttpOnly`, `SameSite=Lax`, `Secure` when `BASE_URL` is `https`).
- **Who acted:** only chore completion and Redemptions record a Member, by avatar tap. Events, to-dos and shopping are anonymous Household activity.
- **Wrong PINs:** 5 wrong tries in a row for an Admin block that Admin's PIN for 5 minutes on every device.
- **Recovery:** any Admin can reset another Admin's PIN in Settings; the last Admin uses `gocasa reset-pin <member>`.
- OAuth tokens, the Immich API key and the Mealie token: AES-GCM encrypted at rest; the master key comes from env and is never stored in the DB or repo. Losing the key = reconnect integrations (documented, acceptable).
- Cross-site requests are refused: Go's `http.CrossOriginProtection` rejects any unsafe (non-GET/HEAD/OPTIONS) request whose `Sec-Fetch-Site` or `Origin` says it came from another site (`BASE_URL`'s origin is trusted). There are no CSRF tokens, so forms and htmx requests carry none.
- No credentials, tokens, or personal values hard-coded in source (placeholder config only — the PuppyPad security.txt lesson).

## 10. Integrations (v1 scope per integration)

| Integration | v1 behaviour |
|---|---|
| Google Calendar | §7.2 — sync in Phase 2a, write-back Phase 2b. Each Household creates its own Google Cloud project and supplies `GOOGLE_CLIENT_ID`/`GOOGLE_CLIENT_SECRET`; GoCasa never ships a shared client. README: External user type + **In production** + unverified (personal-use exemption, under 100 users) — click past the "unverified app" warning once; Testing mode expires refresh tokens after 7 days. Internal also works for Workspace households, same code. Redirect: `${BASE_URL}/auth/google/callback` on an `https` public-suffix name, or a one-time connect from `http://localhost:<port>` / `ssh -L` with `GOOGLE_REDIRECT_URI` overridden |
| iCal/webcal | §7.3, read-only |
| Open-Meteo | Current conditions + daily forecast; lat/lon and units in household settings; no API key needed. Today shows current conditions, today's high/low and 5 days ahead; every other page shows a slim pill (current temperature + today's high/low) |
| Immich | v3.2+ only (older servers get an error in Settings). API key (`x-api-key`) from the account that owns the photos, normally an Admin's (person filtering only works on the owner's library); scopes `asset.read` + `asset.view`, plus `album.read` + `person.read` for the Settings pickers and `memory.read` for Memories (checked at connect). Shows the union of albums and people set to Show and today's Memories (`GET /api/memories?for=<now>`). Albums and people set to Hide (top-level `albumIds`/`personIds` `none`) remove photos from the album and people picks only, not from Memories. Optionally only favorites (`isFavorite:{eq:true}`) and a minimum star rating (`rating:{gte:n}`; unrated photos then drop out), also in the search, so they narrow the album and people picks but never Memories; nothing picked = no Immich photos (never the whole library). `POST /api/search/metadata` with `filter.or` of albumIds/personIds, `trashedAt:{eq:null}`, `visibility:{eq:"timeline"}`, `type:{eq:"IMAGE"}`, paged by `cursor` until `nextCursor` is null; images only. Re-list hourly and on "Refresh now", replacing the cached list. Bytes: `GET /api/assets/{id}/thumbnail?size=preview`, cached on disk (fixed 500 MB, LRU, next 3 prefetched); rotation continues from cache when Immich is down. Shuffle with no repeats, then reshuffle; "Seconds per photo" default 30. The browser never sees Immich credentials |
| Fallback folder | Used only when Immich has nothing to show (not configured, empty set, or unreachable with an empty cache); never mixed in. Fixed path `${DATA_DIR}/photos` (documented as a bind mount); Settings on/off toggle and image count. Subfolders included; JPEG, PNG, WebP only; rescanned hourly and on "Refresh now". A photo over 1920 px on its longest side is shown as an upright JPEG copy that fits 1920 px, made when the scan finds it new or changed and kept in `${DATA_DIR}/photos-screen`; any other is served as-is |
| Mealie | **Mirrored lists**, Mealie v2.0+ (checked via `/api/app/about` at connect; tests on current v3.x). One integration per instance: one Bearer token, one Mealie household; all its lists mirrored, hideable per list. Mealie first, no queue. v1 operations: add (free text in `note`; Mealie items show Mealie's `display`), check/uncheck, delete, "clear checked"; text edits and reordering happen in Mealie. Each check/uncheck re-`GET`s the item, overlays only `checked` and PUTs the whole item back (PUT is a full replace that wipes unsent fields). A 404 re-pulls the list ("That item was removed in Mealie"). After each write apply `deletedItems`/`createdItems`/`updatedItems`; Mealie's merges are accepted. Delete treats already-gone as success; "clear checked" re-pulls first and deletes only items still checked in Mealie. Poll every 60 s (full list GET, diff by id), plus on list open and after each write. When a write fails or 3 polls in a row fail, a "can't reach Mealie" notice shows and Mirrored lists are read-only with their last contents until the next good poll |

New integrations implement one interface:

```go
type Integration interface {
    Type() string
    TestConnection(ctx context.Context, cfg Config) error
    Sync(ctx context.Context, store *Store, emit func(Event)) error
}
```

## 11. Configuration and deployment

Config is env-only (12-factor), no config file to drift. For bare-binary runs, a `.env` in the working directory fills in variables the environment doesn't set:

```
ADDR=:8080
BASE_URL=https://casa.example.com   # how clients reach GoCasa; drives Secure cookies, the Google redirect and absolute links
DATA_DIR=/data
TZ=America/Los_Angeles            # household default; events keep their own IANA zones
APP_ENCRYPTION_KEY=<base64 32-byte key>
GOOGLE_CLIENT_ID= / GOOGLE_CLIENT_SECRET=
GOOGLE_REDIRECT_URI=              # optional; defaults to ${BASE_URL}/auth/google/callback, override for the localhost / ssh -L route
SESSION_SECRET=
```

- **Docker:** multi-stage build → final image `scratch` (or `distroless/static`) containing only the binary + CA certs + tzdata. `CGO_ENABLED=0`. Image target: < 30 MB.
- **Compose (dev + Gordon):** one service, one volume (`/data`), optional bind mount for `/data/photos`, `healthcheck` on `/healthz`. Runs under Dockge on Gordon like the rest of the stack; can equally be a Coolify resource later.
- **Data volume:** `app.db`, `backups/`, `photo-cache/` (500 MB Immich byte cache, safe to delete), `photos/` (Fallback folder).
- **HTTPS:** GoCasa serves plain HTTP only; the operator's reverse proxy terminates TLS. GoCasa ignores `X-Forwarded-*` headers and takes its own address from `BASE_URL`.
  - *Why:* Screen Wake Lock and webcam motion-wake need a secure context, and Google OAuth needs an `https` redirect on a public-suffix domain. Over plain HTTP everything else works; those features stay off and Settings says HTTPS turns them on.
  - *Recipe:* a name on a domain you own (e.g. `casa.<yourdomain>`), local DNS (router, Pi-hole, AdGuard) pointing it at the host's LAN IP, and a reverse proxy holding a real certificate (e.g. Let's Encrypt via DNS-01). The name needn't resolve publicly: the Google redirect happens in the browser.
  - *Worked example:* Nginx Proxy Manager proxy host → `http://<host>:8080`, Let's Encrypt DNS-01 cert. No custom config: the SSE endpoint handles buffering and idle timeouts itself (§8).
  - *Out of scope:* remote (off-LAN) access. That's the homelab's job (tunnel, VPN, authenticating proxy); GoCasa documents none of them.
- **tzdata is embedded** (`time/tzdata`) so DST rules never depend on the host image.
- **Backup:** SQLite in WAL mode. An in-app scheduler runs `VACUUM INTO '/data/backups/app-YYYYMMDD.db'` at a fixed 03:00 local time and keeps the last 14 (neither configurable). At startup, if the newest backup is over 24 hours old, one is taken immediately. Restore = stop container, swap file, start. Tested restore is part of Phase 0 acceptance, not an afterthought.
- **Upgrade:** replace the binary/image; embedded migrations run at startup, one transaction each, after an automatic pre-migration backup (same backup function). Rollback = previous image + pre-migration backup. A DB newer than the binary refuses to start.

## 12. Build phases (each phase ends usable, not just compiling)

### Phase 0 — Skeleton
Binary serves `/healthz` + one templ page; SQLite opens, migrations run; nightly backup; Docker image + compose; backup/restore proven once by hand.
**Accept:** fresh `docker compose up` → page loads; kill + restore from backup → same page; the binary refuses a DB with a higher `user_version`; a missed nightly backup is taken at startup.

### Phase 1 — Local calendar core
Members (incl. avatars, archive), household settings, local calendars, event CRUD (incl. recurrence, One-off edits, edit scopes and Split), month/week/day/agenda views, Member filters, per-device Default view + `?date=`/`?users=` links, SSE live reload.
**Accept:** two browsers open; create/edit/delete (incl. each edit scope) in one, the other updates without refresh; DST-boundary, all-day and Split cases in §13 pass.

### Phase 2 — Sync: iCal, then Google
2a: iCal feeds (ETag, soft-delete) → Google sync (singleEvents + syncToken, window, daily re-import, 410 fallback), Reconnect needed, stale-sync notice, "Sync now". Settings UI for integrations, encrypted credentials, per-integration status/errors.
2b: Google write-back with `If-Match` and the Google Split.
**Accept:** a real household Google calendar and one public iCal feed stay correct through: a recurring series, one edited occurrence upstream, one deleted occurrence upstream, and a forced full re-sync. Plus the 5-step singleEvents test in `docs/research/google-single-events-sync.md` (how far ahead Google expands never-ending series, whether occurrences moving into the window arrive incrementally, what a master time change returns, how deleting a whole series appears), a split made in Google's own app as it arrives through sync, a GoCasa This-and-following on a Google series, and a 412 conflict.

### Phase 3 — Todos and shopping
Todo columns + Recurring to-dos + reorder/sort/filter; shopping lists local-first; Mealie Mirrored lists behind the same list UI.
**Accept:** reorder persists across restart; completing a Recurring to-do creates the next copy exactly once; Repeat from schedule skips missed dates and isn't consumed by early completion; un-completing removes the open next copy and refuses when the next is completed; a live Mealie round-trip (check, uncheck, add, delete, clear checked) preserves every item field and handles a merge and an outage.

### Phase 4 — Screensaver, weather, Locks
Immich screensaver + Fallback folder, Wall display idle/wake, Night mode, camera motion-wake, Today page, Open-Meteo header and pill, Admin PINs, Locks, lock screen.
**Accept:** an idle Wall display rotates photos with clock/agenda, and a non-Wall-display browser never does; Fallback folder shows only when Immich has nothing; Night mode switches at its hours; motion wakes the screensaver over HTTPS; Settings needs an Admin PIN; each Lock off and on; the 5-minute idle relock; 5 wrong PINs block that Admin for 5 minutes; Today shows today/tomorrow, today's Chores, and due and overdue to-dos; weather renders from cached data with the network cable pulled.

### Phase 5 — Chores/rewards and polish
Chores (Each/Anyone, Rounds, Late/Missed), points, Rewards, Redemptions, Adjustments; preferences polish; ICS export of a calendar view.
**Accept:** a Late Round can be claimed until the next Round ends, then is Missed; an Anyone chore completes once per Round; an Admin undo removes the points; a Redemption spends nothing until approved and can't go below zero; full household day-in-the-life pass on phone, tablet, and wall display sizes.

## 13. Testing — where clones actually fail

Boundary testing is a release gate, in the house style: test every boundary the code distinguishes, and state which cases were already covered vs newly added.

Mandatory case sets before any phase is "done":

- **Recurrence (local):** daily/weekly/monthly/yearly, INTERVAL > 1, BYDAY ordinal (2nd Tuesday, last Friday), COUNT vs UNTIL, EXDATE + RDATE, edited Occurrence, deleted Occurrence, series that crosses a DST change in both directions, a 02:30 series on spring-forward day (skipped) and a 01:30 series on fall-back day (first offset)
- **Edit scopes:** a Split with One-off edits after it (reset with the warning count), a text-only All events edit with One-off edits (they keep their changes), a Split on a Series' first Occurrence, This-and-following delete
- **Time:** all-day multi-day events, events at 00:00 and 23:59, UTC↔America/Los_Angeles conversions in DST gap and overlap, a feed whose events carry a foreign TZID
- **Sync:** first import, no-change sync, upstream edit, upstream delete, cancelled Google instance, window edges (−3 / +12 months), expired Google syncToken (410), `invalid_grant` vs transient errors, `If-Match` 412, feed returning 304, feed returning garbage, sync interrupted mid-transaction, Member override surviving an upstream edit
- **Mealie:** PUT preserves `extras`/`recipeReferences`, merge returning a new id, 404 on check, 3 failed polls → read-only
- **Limits (test at, below, and above):** title/description length caps, max calendars per integration, max occurrences expanded per series, shopping/todo reorder at first/last position, photo cache at 500 MB
- **Concurrency:** two clients editing the same local event; SSE client disconnect/reconnect catching up

Unit tests cover expansion/timezone logic (pure functions, golden files). HTTP handlers get `httptest` coverage for CRUD + permissions (Locks, Admin-only Settings). No e2e browser suite in v1 — manual phase-acceptance passes above instead.

## 14. Risks and open questions

| # | Item | Handling |
|---|---|---|
| 1 | rrule-go edge cases (ordinal BYDAY + DST) | Now local-only: Google calendars use Google's own occurrences. Golden-file tests plus the RFC 5545 gap/overlap fixes (§7.1); vendor or fork rrule-go if a bug appears (unmaintained since 2023) |
| 2 | Drag-and-drop richness in htmx vs a Vue app | v1: drag for todo/shopping reorder (SortableJS, vendored); calendar event drag is Phase 5 polish, click-to-edit is v1 |
| 3 | Google OAuth for a self-hosted app | External + In production + unverified (personal-use exemption); Testing mode is **not** sufficient (7-day token expiry). Not live-tested past 7 days: confirm during Phase 2 |
| 4 | Write-back conflicts (event edited in Google and in-app between syncs) | `If-Match` etag on every write; a 412 reloads and asks the person to re-apply (§7.2) |
| 5 | Google's split and singleEvents behaviour is partly unverified | Settled by the Phase 2 acceptance tests |
| 6 | Immich `filter` exclusions were read from source | Check once against a live server in Phase 4 |
| 7 | Project name | **Resolved 2026-10-18: GoCasa** (binary/repo `gocasa`) |
| 8 | SkyLite fork PR #80 | **Resolved:** the fork stays as reference only; housekeeping is out of scope |

## 15. License

Clean-room implementation of behaviour; no upstream code copied. License is Brandon's choice — AGPLv3 recommended for consistency with PuppyPad and PlaylistGen.
