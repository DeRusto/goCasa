# Research: OpenFamily compared with GoCasa

Researched 2026-11-19. GoCasa at `main` `90daff5`; [OpenFamily](https://github.com/NexaFlowFrance/OpenFamily) ([openfamily.fr](https://openfamily.fr/)) at `54c3177` (v2.2.0). Both projects are AGPL-3.0, so porting code across is licence-compatible as long as copyright notices are kept. In practice the stacks differ (Go vs TypeScript), so what GoCasa can borrow is mostly design.

## Short answer

- **The two projects are different products that overlap in the middle.** OpenFamily is a *family organiser*: accounts, a phone app, recipes, a budget, a pantry and an AI assistant, with a kiosk as one of many views. GoCasa is a *wall display*: one Household, no logins, and a screen that stays on. They overlap in the calendar, chores and points, shopping lists, the kiosk and screensaver, weather, live updates and Immich and Mealie.
- **Where they overlap, GoCasa usually goes deeper.** It has a full RRULE engine with golden tests and two-way Google sync, Mealie shopping lists that stay in sync both ways, a more precise chore model (Rounds, Each/Anyone, Late/Missed), more Wall display features (Wake Lock, Night mode, motion-wake, Photo pairs, Memories, Fallback folder), automatic backups and a much smaller footprint.
- **Where OpenFamily is ahead, it's about being open to other software and easy to install.** It has a token API, webhooks, display links for e-paper screens, catalog packaging for Home Assistant, Unraid, CasaOS, TrueNAS and others, and secrets it generates on first start.
- **Worth adopting, in priority order:**
  1. Outgoing webhooks driven by the existing live-update `Bus`.
  2. A small read-only JSON API with hashed tokens.
  3. An e-paper display page that needs no JavaScript.
  4. Secrets generated on first start.
  5. Birthdays computed from Members.
  6. Catalog packaging.
  7. Savings goals and streaks for the Kid style Member page.
  8. Hardening outbound fetches.
- **Not worth adopting:** accounts, JWT and OIDC; the budget, pantry, recipes and AI assistant; web push; a Postgres-based stack. Each of these goes against a GoCasa principle (no logins, delegate to Mealie, one binary and one SQLite file) or is out of scope.

---

## 1. The two projects at a glance

| | GoCasa | OpenFamily |
|---|---|---|
| Pitch | Self-hosted family *wall calendar* for a homelab | Self-hosted family *organiser*, a Cozi/FamilyWall alternative |
| Tenancy | One Household per instance | Many families per server (owner account + invited accounts); also sold as a hosted cloud |
| Identity | No logins. Anyone at the screen views and ticks things off; Admins have a PIN; optional **Locks** per action kind | Email/password accounts (bcrypt, JWT 7 days), invites, OIDC SSO, parent/child roles |
| Backend | Go, `net/http`, templ, HTMX + SSE, Tailwind (standalone CLI, no Node) | Node 26, Express, `pg`, `ws`, node-cron, nodemailer, web-push, `@anthropic-ai/sdk` |
| Frontend | Server-rendered HTML | React 19 SPA, Vite, Radix, i18next, PWA with offline cache |
| Storage | One SQLite file, 24 numbered migrations, `PRAGMA user_version` | PostgreSQL 16; idempotent DDL list in `db.ts` + versioned, checksummed migrations |
| Deploy | One static binary / `FROM scratch` image (amd64, arm64), required secrets in `.env` | All-in-one image (Node + Postgres inside) **or** three-container compose; Windows `.exe` installer; Android APK (Capacitor); catalog entries for HA, Umbrel, CasaOS, Runtipi, Unraid, TrueNAS, Proxmox script |
| Size | ~32k lines of Go (incl. tests and generated templ) | ~65k lines excluding locales and lockfile |
| i18n | English only | 8 languages, partial translations fall back to English |
| Tests | `internal/apptest` builds the real app with a fake clock and fake upstreams; recurrence golden files | Server unit/integration tests (`server/test`), CI smoke tests of API and all-in-one image |

## 2. Feature matrix

✅ has it · ◐ partial · ✗ doesn't

| Area | GoCasa | OpenFamily | Notes |
|---|---|---|---|
| Local calendar, month/week/day/agenda | ✅ | ◐ month + week planning | |
| Recurrence | ✅ full RRULE (BYSETPOS, last Friday, DST, EXDATE/RDATE, golden tests), Edit scope, Split | ◐ freq+interval+until; complex imported rules are expanded into single events in a window | GoCasa is clearly ahead |
| Event time zones | ✅ events keep their own zone; Household default | ◐ naive wall-clock times + one family zone | GoCasa's model is more correct for travel and imported events |
| Google Calendar | ✅ two-way, several accounts, own OAuth client | ◐ read-only via the "secret iCal address" | |
| iCal/webcal subscribe | ✅ | ✅ (every 30 min, UID + content hash dedupe, local members kept) | Same idea, both keep local member choices across syncs |
| .ics import of a file | ✗ | ✅ | Small gap; rarely needed on a wall display |
| ICS export feed | ✅ `/calendar.ics` | ✅ (per-token webcal URL) | |
| Nextcloud CalDAV | ✗ (works as an iCal feed) | ✅ import with discovery | |
| Birthdays | ✗ | ✅ derived from dates of birth, shown everywhere, morning notification | **Adopt** (§4.5) |
| Reminders / notifications | ✗ | ✅ up to 5 per appointment, web push + email | Out of scope for a wall display; phones get reminders by subscribing to `/calendar.ics` |
| Chores | ✅ Rounds, Each/Anyone, Late/Missed, Routines, per-weekday | ◐ tasks with frequency, points, categories, optional "pending approval" | GoCasa's model is richer |
| Points & rewards | ✅ balances, Rewards, Redemptions approved by Admin, Adjustments | ✅ ledger, adjust/redeem, **savings goals**, **streaks**, currency (pocket money) | **Borrow goals + streaks** (§4.7) |
| To-dos | ✅ columns, drag order, due dates, Repeat from schedule/completion | ◐ tasks double as to-dos | |
| Shopping lists | ✅ local + Mealie Mirrored lists (two-way) | ✅ categories, prices, templates, store aisle mode; HA and Grocy sync | GoCasa delegates rich shopping to Mealie |
| Recipes, meal planning, pantry, budget, Kakeibo | ✗ | ✅ | Out of scope: GoCasa delegates to Mealie |
| Family notes / posts feed | ✗ | ✅ | Notes on the Today page could suit a wall display; low priority |
| Weather | ✅ server-side Open-Meteo, own page, metric/imperial | ✅ client-side Open-Meteo in kiosk | |
| Kiosk / Wall display | ✅ Wake Lock, idle screensaver, Night mode (black/dim clock), camera motion-wake, per-device Default view | ◐ full-screen kiosk, screensaver after N min (photos or moving clock), per-device settings in localStorage | GoCasa ahead |
| Screensaver photos | ✅ Immich albums/people, Memories, Photo pairs, Fallback folder, cache survives Failing | ◐ Immich photo (random), or a clock | GoCasa ahead |
| E-paper / no-JS display page | ✗ | ✅ **display links** | **Adopt** (§4.3) |
| Live updates | ✅ SSE, typed `web.Kind`, heartbeat, slow-client drop | ✅ WebSocket, typed entity + action | Same design: "say what changed, client refetches" |
| JSON API + tokens | ✗ | ✅ hashed `ofk_` tokens, read/write scope, OpenAPI served | **Adopt a small one** (§4.2) |
| Outgoing webhooks | ✗ | ✅ HMAC-signed, coalesced, auto-disabled after 20 failures | **Adopt** (§4.1) |
| Home Assistant | ◐ via `/calendar.ics`; add-on researched | ✅ shopping/todo sync over HA WebSocket; HA add-on; Gladys integration | |
| Backups | ✅ nightly `VACUUM INTO`, 14 kept, pre-migration snapshot, refuse to start on failure | ◐ manual full JSON export/import per family; no scheduled backup in-app | GoCasa ahead |
| Secrets setup | ✗ user runs `openssl` twice | ✅ generated on first start, stored with the data | **Adopt** (§4.4) |
| Integration credential encryption | ✅ `APP_ENCRYPTION_KEY` | ✅ (also encrypts webhook URLs) | |
| SSRF protection on user URLs | ✗ | ✅ metadata endpoints blocked, redirects re-checked per hop, body caps | **Borrow parts** (§4.8) |
| Mobile app | ✗ (WebView shell researched) | ✅ Capacitor APK, PWA | |
| Live demo | ✗ | ✅ static SPA + mock API on GitHub Pages | Hard for a server-rendered app; skip |
| AI assistant | ✗ | ✅ Ollama/OpenAI/Anthropic/Gemini | Skip |

## 3. Where they overlap: implementation compared

### 3.1 Live updates

Both projects arrived at the same design separately. A mutation publishes a coarse *kind* (GoCasa `web.Kind`: `event`, `todo`, `list`, `chore`, `points`, ...; OpenFamily `WsEntity` + `created|updated|deleted|synced`). Clients refetch what they show, and no data travels over the channel. GoCasa's `Bus` (`internal/app/live.go`) is the more defensive of the two: `Publish` never blocks, a lagging stream is dropped and reconnects, and a 25 s heartbeat beats proxy idle timeouts. OpenFamily needs WebSocket proxying and session revocation (close code 4001). GoCasa has no sessions to revoke.

**What OpenFamily does with it that GoCasa doesn't:** `onBroadcast(listener)` lets the webhook module subscribe to the same stream (`server/src/lib/webhooks.ts`). GoCasa's `Bus` could take a second subscriber the same way. That subscriber is the whole hook for §4.1.

### 3.2 Calendar import and sync

- **Identity across syncs:** OpenFamily keeps `external_uid` + `external_hash` (a fingerprint of the content), updates only what changed and removes what the source dropped. Member choices, reminders and notes survive. GoCasa's spec §7.1 does the same with `local_override`.
- **Recurrence:** OpenFamily maps an imported RRULE onto its own simple model (`daily|weekly|monthly|yearly` × interval × until). It splits a multi-day weekly rule into one series per day, and **expands anything else into single events within a window** (`lib/icsCalendar.ts` header). That's a deliberate simplification: "2nd Tuesday" events become loose copies that stop at the window edge. GoCasa's engine handles those rules natively, with golden tests for DST gaps and overlaps, leap days, COUNT gaps and BYSETPOS. **Nothing to borrow here. GoCasa's approach is the better one.**
- **Time model:** OpenFamily stores naive wall-clock times and gives them a zone only when a moment is needed (reminders, feeds). A per-family zone came in v2.0. GoCasa stores each event's own zone. OpenFamily's `timeZone.ts` comment documents the bug class it had to fix, "the server's [zone] was the only one, which is wrong as soon as the server runs somewhere else". GoCasa avoided that from the start (`0023_household_timezone.sql`).
- **Feed hardening:** OpenFamily caps a feed at 10 MB, 5,000 events and 10 subscriptions, with a 20 s fetch timeout. GoCasa's `syncClient` already has a byte `limit` and sorts errors into transient and permanent, which OpenFamily doesn't. **Check that GoCasa caps the number of events per feed**, so a runaway feed can't fill the SQLite file.

### 3.3 Chores, points, rewards

| | GoCasa | OpenFamily |
|---|---|---|
| Unit of work | Chore with Rounds; Each vs Anyone; Late (claimable until next Round ends) then Missed | Task with a `frequency`; completing a recurring task resets it |
| Points | Per completion, per Member | Ledger `reward_transactions` (`earn`, `adjust`, `redeem`) |
| Approval | Redemptions need an Admin | Task completion can be `pending_approval` before points land |
| Spending | Rewards with fixed cost → Redemption | Redeem any amount; **savings goals** with progress bars; optional currency (pocket money) |
| Motivation | Routines (Morning/Afternoon/Evening) | **Streaks**: consecutive days with an `earn`, ending today or yesterday |

GoCasa's model is better specified. OpenFamily's own code admits it has no per-member completion history ("`tasks.completed_at` is reset when a recurring chore is un-completed"), so it computes streaks from the points ledger. GoCasa keeps a completion per Round per Member, so streaks would fall straight out of existing data. See §4.7.

Approving *completions*, not just redemptions, is the one idea here GoCasa doesn't have. Families with young children ask for it: "Did you really brush your teeth?" It would need a new state on a completion. Consider it only if users ask.

### 3.4 Kiosk / Wall display

OpenFamily's kiosk (`client/src/pages/Kiosk.tsx`) is a React page with per-device settings in `localStorage`, Open-Meteo fetched straight from the browser, and a screensaver after 1–60 minutes showing either an Immich photo or a "moving clock". It has no Wake Lock, Night mode, motion-wake, photo cache or fallback. GoCasa's Wall display is ahead on every axis. Two small ideas:

- **A moving clock as a screensaver style.** It moves to avoid burn-in on OLED and cheap LCD panels. GoCasa's Night mode "dim clock" could drift its position slowly for the same reason, if it doesn't already.
- **Fahrenheit by region by default.** OpenFamily picks °F for US territories from the locale. GoCasa makes the Admin choose, which is fine.

### 3.5 Integrations

| | GoCasa | OpenFamily |
|---|---|---|
| Mealie | **Shopping lists, two-way (Mirrored lists)**; Down → read-only | Recipe import only (paged, v1 and v2) |
| Immich | Albums, people, Memories, filters, cache | One random photo for the kiosk background |
| Home Assistant | Remote Calendar can subscribe to `/calendar.ics` | Shopping/to-do sync over HA's WebSocket (`todo/item/list`), several lists |
| Others | Google, iCal, Open-Meteo | Tandoor, Grocy, Nextcloud CalDAV, Gladys |
| Health | Failing (one failed sync) / Down (three in a row) states | Per-integration last-sync status |

GoCasa's Integration health model (Failing and Down, and what each means for the UI) is a clear strength.

OpenFamily's **HA to-do sync** is the most interesting idea here. HA's `todo` entities (2023.6+) are what HA users already use for shopping, so a GoCasa local list could mirror one the way Mirrored lists mirror Mealie. GoCasa's `syncClient` and the Mirrored list pattern already cover most of the shape. **Only build it if someone asks.** The HA research doc's position (HA can read GoCasa's calendar today) still holds.

### 3.6 Data safety

GoCasa: automatic nightly `VACUUM INTO`, 14 kept, a pre-migration snapshot, refuse to migrate without one, refuse to open a newer schema. OpenFamily: a manual per-family JSON export and import that leaves out secrets, maps accounts by email and is idempotent. It has no scheduled backup inside the app (the Postgres volume is left to the operator).

One small idea worth taking: OpenFamily's migration runner stores a **checksum of each applied migration** and refuses to start if a shipped migration's SQL was later edited (`server/src/migrations/runner.ts`). GoCasa embeds its migrations and tracks only `user_version`, so editing `0012_weather.sql` after release would go unnoticed on databases already past 12. A test that hashes `internal/db/migrations/*.sql` against a committed list would catch this at CI time without changing the schema. It's cheap insurance, but optional.

## 4. Recommendations

Ordered by value to a homelab wall-display user against how much new code each needs. Every item keeps GoCasa's principles: one binary, one SQLite file, no logins for viewing, and outside tools doing their own jobs.

### 4.1 Outgoing webhooks from the Bus (adopt)

Why: Home Assistant, n8n and Node-RED users want "chores all done → turn on the TV" or "shopping list changed → notify" without polling. OpenFamily's design copies over almost directly, because GoCasa's `Bus` already publishes exactly the right signal.

Borrow from `server/src/lib/webhooks.ts`:
- The body says only **what kind changed** (`{"event":"chore","at":...}`), never the data. The receiver reads through the API (§4.2). Household data never goes to a URL that may have changed hands.
- **HMAC-SHA256 signature** of the exact body with a per-webhook secret (`X-…-Signature: sha256=<hex>`).
- **Coalescing**: changes of the same kind within ~1 s are sent once.
- **Auto-disable** after N consecutive failures, shown in Settings, the same way GoCasa shows a Down Integration.
- **Encrypt the URL at rest** like the secret. An HA webhook URL is itself a credential. GoCasa's `APP_ENCRYPTION_KEY` path already does this for tokens.
- A "Send test" button.

Size: one table, one Bus subscriber goroutine, a Settings section. If §4.2 isn't built, webhooks are still useful on their own: an HA automation can react to "chore changed" by itself.

### 4.2 A small read-only JSON API with tokens (adopt, small)

The HA research doc already proposed `/api/summary` for `rest` sensors. OpenFamily shows the token design to use:
- A random token with a **recognisable prefix** (`ofk_` there), shown **once**, with **only its SHA-256 stored**.
- A **scope** (read / write). Start with read only.
- A token **can never reach settings, integrations or other credentials**, whatever its scope (`apiTokenRefusal`). A leaked token gives away Household data, never control.
- Revocable in Settings, and capped in number.

Keep it to what a dashboard or automation needs: today's events, chores and their state, balances, list items. Don't serve OpenAPI or offer write endpoints until someone asks. Viewing in GoCasa is never locked, so on a LAN the token mostly guards against exposing the API through a reverse proxy. Even so, the token keeps the API off unauthenticated paths, so a public proxy doesn't hand out Household data by accident.

### 4.3 E-paper display page (adopt)

OpenFamily's **display links** (`server/src/lib/displayPage.ts`, `displaySnapshot.ts`) are one complete HTML document with **no script**, built for InkyPi-style e-paper frames, e-readers and screenshot tools that capture a page before any JavaScript runs. Details worth copying:
- **Black on white with no grey text**, because e-paper dithers grey.
- Type that scales with the viewport. **What doesn't fit is left out whole, never cut through a line.**
- `?sections=` to choose and order the blocks, `?refresh=` for a `<meta refresh>` (clamped 30 s – 1 day), `?scale=` (0.5–2.5).
- Shows today **and tomorrow**, "since a screen that is only glanced at in the evening is mostly asked about the next morning".

GoCasa already renders on the server with templ, so this is one more templ page reusing the Today data, at something like `/display`. Since viewing is never locked, it needs no token. It fits the homelab audience well.

### 4.4 Generate secrets on first start (adopt)

OpenFamily's one-line `docker run` works because secrets are made on first start and kept with the data. GoCasa's quick start needs `openssl rand` twice, and the HA add-on research (§1.2 item 3) already proposes generating them into `/data/secrets.env`. OpenFamily shows this works in practice across HA, Unraid, CasaOS and other catalogs. Keep the env vars as the override. Document the trade-off in the README: the key then lives in the same backup as the data, so a stolen backup can decrypt integration tokens.

### 4.5 Birthdays from Members (adopt, small)

Give a Member an optional date of birth and compute a yearly all-day "Name's birthday (age)" occurrence wherever the calendar is read: Today, calendar, `/calendar.ics`, the display page. Never store it as an event, the way OpenFamily's `lib/birthdays.ts` does it. Pets count too, since GoCasa Members include them. It saves the family typing birthdays twice and touches no sync code.

### 4.6 Catalog packaging (adopt, after 4.4)

OpenFamily's `packaging/` directory and companion repos give it one-click installs on Home Assistant OS, Umbrel, CasaOS/ZimaOS, Runtipi, Unraid, TrueNAS and Proxmox. That has probably done more for its adoption than any feature. GoCasa's single static image is an even easier fit. Start with the HA add-on (already researched) and an Unraid template. Both are config files around the existing image, once §4.4 removes the required secrets.

### 4.7 Savings goals and streaks on the Member page (consider, for #107)

The Kid style Member page (issue #107) is where these belong:
- **Streak:** consecutive days with at least one completed Chore, ending today or yesterday, because today's chore may not be done yet. GoCasa can compute it from completions. It doesn't need a ledger workaround like OpenFamily's.
- **Savings goal:** a Member picks a Reward to save toward, and the page shows a progress bar of balance against cost. In GoCasa terms this is a pointer to a Reward, not a new money concept. Leave OpenFamily's currency and pocket-money layer out.

### 4.8 Harden outbound fetches (consider, small)

GoCasa fetches Admin-supplied URLs (iCal feeds, Mealie, Immich) with Go's default client, which follows redirects. LAN targets are legitimate, as OpenFamily also concludes ("RFC1918 addresses are allowed by default"), so don't block them. But OpenFamily's `urlGuard.ts` + `safeFetch.ts` do two cheap things worth copying:
- **Always block cloud metadata addresses** (`169.254.169.254`, `fd00:ec2::254`, `100.100.100.200`, `metadata.google.internal`), checking both the literal host and the resolved addresses.
- **Re-check every redirect hop** with the same rule. In Go, use `http.Client.CheckRedirect`, or better, a `net.Dialer.Control` func that checks the IP actually dialled. That also closes the DNS-rebinding gap OpenFamily admits it leaves open.

The risk is low: only an Admin with the PIN can add a URL. It matters more for anyone running GoCasa on a cloud VM.

### 4.9 Considered and not recommended

- **Accounts, JWT, OIDC, parent/child roles.** GoCasa's PIN, Locks and Admin model is a deliberate product choice for a shared screen. OpenFamily needs accounts because it's multi-family and phone-first.
- **Recipes, meal planning, pantry, budget, Kakeibo, posts.** These are large modules (budget alone is ~2.8k lines in OpenFamily). GoCasa delegates food to Mealie. Budget and posts don't belong on a wall display.
- **AI assistant.** It adds providers and keys and costs much more to maintain than the rest of this list.
- **Web push, email reminders.** These need per-person devices and subscriptions. Phones already get reminders by subscribing to `/calendar.ics`.
- **Postgres or an SPA.** OpenFamily needs an all-in-one image with Postgres inside just to reach "one command", which GoCasa gets for free.
- **Store aisle mode, price tracking.** These belong to Mealie.
- **A static in-browser demo.** OpenFamily mocks its whole API in the SPA (`client/src/demo/mockApi.ts`). GoCasa's server-rendered pages would need a hosted instance instead. Not worth it now. The screenshot seed in `docs/screenshots/seed.sql` covers the README.

## 5. Process and community practices worth noting

These aren't code, but they're cheap and visible:
- **Partial translations accepted, missing strings fall back to English** (`TRANSLATING.md`, `client/scripts/check-i18n.mjs`). Not relevant while GoCasa is English-only, but it's the approach to take if that changes.
- **Issue forms and Discussions** for ideas vs bugs. GoCasa already has a triage-label process (`docs/agents/triage-labels.md`).
- **`AI_USAGE.md`**: a public statement of which PRs were AI-assisted and what was actually tested. Worth considering given how GoCasa is developed.
- **"What a release was validated on"** records per platform: which catalog installs were actually tested and which weren't. Useful once §4.6 exists.

## 6. Suggested order

1. §4.4 secrets on first start, then §4.6 HA add-on + Unraid template. These remove setup friction.
2. §4.5 birthdays and §4.3 e-paper page. These are small, visible and fit the wall-display identity.
3. §4.1 webhooks, then §4.2 read-only API, if homelab users ask for automation.
4. §4.7 inside the Member page work (#107).
5. §4.8 and the migration checksum test whenever convenient.
