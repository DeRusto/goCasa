# Research: what owners of paid family displays complain about, and what it means for GoCasa

Researched 2026-11-23 against GoCasa `main` at `a709af3`. The input is a research spreadsheet made outside this repo (`family-display-pains.xlsx`, refreshed 23 Nov 2026), which this note does **not** reproduce. It had 117 dated app-store review excerpts for Skylight, Hearth, Cozyla and DAKboard, 6 extra owner quotes and 8 self-hosted asks. I re-checked every claim it made about GoCasa against the code, re-themed all 117 reviews myself and re-ranked the results. Builds on `docs/research/home-assistant-and-android.md` (PWA, the ICS feed in Home Assistant) and `docs/research/kiosk-satellite.md` (motion wake, face crops, display recovery, remote device settings), which are not repeated here.

Every claim about GoCasa cites a file. Every review is quoted from the spreadsheet with its reviewer, date and the store page it came from. My own inferences say so, and claims I couldn't check are marked **unverified**.

## Short answer

- **The spreadsheet undersold GoCasa.** It judged coverage from the README alone. In the code, GoCasa already has most of what these owners pay a subscription for, or complain is missing: event locations, split colours for multi-Member events, a "+" on every day, editing Google events with Members on them, sync status in Settings and on the calendar, dark mode, and an Immich screensaver with Memories. **48 of 117 reviews (41%) are already solved, and 8 more partly.** Those are README selling points, not roadmap items.
- **Its top two gaps were inflated.** "Calendar sync & reliability" (16) held only 6 real sync complaints with other calendars. Two of them name iPhone or Apple, and GoCasa's two-way Google sync covers part of two more. The rest were app crashes, Wi-Fi, a photo-upload crash and shopping-list drift. "Calendar entry & navigation" (15) is almost entirely solved.
- **The real gaps, ranked by how strong the evidence is and how well each fits GoCasa:**
  1. **Event reminders** (7 reviews across 3 products, 4 of them asking for several alerts or custom ones per event; plus 1 asking to be told when an event changes). No nagging. Showing them on the wall: **small–medium**. Sending them to phones is a new scope decision: the spec allows "in-app display only" in v1.
  2. **Phone access: an installable web app** (8 reviews that mainly want a widget, 3 more that mention one; 4 of the 8 about calendar widgets. No review asks to install a web app: that is my nearest web answer). Plus telling people that `/calendar.ics` already feeds the phone's own calendar widget. **Small**, but it needs HTTPS.
  3. **Meal plans read from Mealie** (8 meal reviews, but only 1 that a read-only view answers directly and 2 next to it; plus 2 self-hosted sources). Show tonight's dinner and the week's meals, and keep planning in Mealie. **Medium.**
  4. **Event search** (4 reviews, all Hearth). **Small.**
  5. **Two-way Apple/iCloud sync** over CalDAV (2 reviews that name Apple or iPhone, plus 2 broader sync reviews and 1 iOS setup ask). The spec already names it a "Phase 6 candidate". **Large.**
  6. **Show event locations** in the views, not only in the edit form. **Tiny.** It also makes an existing feature visible.
  7. **Stopping children from completing a sibling's chores** (1 direct review) and **an on-screen note** (2 reviews). Thin evidence, and each runs into a domain rule that needs deciding first.
- **Don't pursue:** voice assistants, AI import, a grocery-store API, budgeting, video messages. They are thin and break the "no cloud" rule. Also not ranked: steps inside one chore (1 review) and videos on the screensaver (1 review).
- **The sample can't support ranking by raw counts.** It was chosen by hand, it is mostly about the companion phone apps rather than the displays, and the self-hosted evidence is three sources. Treat these counts as signals, not market share.

---

## 1. Method and caveats

**What I did**

1. Read all 117 rows of the "Paid display feedback" sheet and gave each one my own **primary theme**: the main pain or wish in the quote. A review that mentions a second thing is noted in the text below, but counted once.
2. Gave each review a **verdict** against GoCasa today:
   - **gap**: GoCasa doesn't do it, and it fits GoCasa.
   - **solved**: GoCasa already does it. A selling point.
   - **partly**: some of it exists.
   - **n/a**: a vendor's own bug, subscription or login problem, or something outside what GoCasa is.
3. Checked each verdict in `internal/`, `GLOSSARY.md`, `docs/skylite-go-spec.md` and `docs/research/`, and checked `gh issue list --state all` (#1–#113) so nothing already tracked or decided is proposed again.
4. Ranked the gaps by evidence strength (how many reviews, across how many products, how specific) **and** fit with GoCasa's constraints: pure Go, a single binary, SQLite, no cloud, a browser client. Not by raw count.

The full row-by-row mapping is in the [appendix](#appendix-every-review-re-themed).

**Caveats about the sample**

- **Purposive, not random.** The sheet itself says: "Selection is purposive, not random; counts are frequencies within this curated sample, not market-wide rates." Products: Skylight 40, Hearth 38, Cozyla 38, DAKboard 1. With only one DAKboard row, nothing here speaks for DAKboard owners.
- **Mostly companion phone apps.** Every primary row is a review of the vendor's **phone app** (App Store or Google Play), not of the display. The sheet notes that hardware ownership is "not independently verified". So phone-side asks (widgets, push reminders) are over-represented compared with what people want from the screen on the wall.
- **Links go to the store's review list, not to the single review.** App Store and Google Play have no permanent link to one review. Each citation below names the reviewer and date to find it there. The excerpts are the spreadsheet's. I did not re-fetch them, and the stores may have trimmed or reordered reviews since.
- **The self-hosted evidence is thin.** One Home Assistant forum thread title (the forum blocked fetching), one open GitHub PR, and one 2022 blog post by a paying DAKboard owner. Reddit quotes came second-hand through a review site. I use them only to back up a signal, never to start one.
- **One theme per review** hides second asks. Search, notes and Apple sync each pick up extra mentions as secondary asks, which I count separately where it matters.

## 2. Where the spreadsheet was wrong about GoCasa

| Spreadsheet said | The code says |
|---|---|
| "The README does not list event locations" (Event details & location: **No**) | Events have a Location field in the form (`internal/web/calendar.templ` line 766). It is stored (`internal/app/events.go` `eventChange.location`), synced both ways with Google (`internal/app/google_sync.go` `googleEvent.Location`, `internal/app/google_write.go` `googleBody`), read from iCal feeds (`internal/app/ical.go` `parseVEvent`) and exported in the ICS (`internal/app/ics.go` `PropLocation`). **What's missing:** no view shows it. See §4.6. |
| "No documented sync-status or conflict-repair UI" | Every Integration shows "Last synced" and, while failing, "Sync failing (N in a row): *error*" in Settings (`internal/web/settings.templ` `syncStatus`, fed by `last_error`/`consecutive_failures` in `internal/app/integrations.go`). The calendar puts a banner over a stale calendar: "last updated …, Syncing keeps failing" or "Reconnect needed" (`internal/web/calendar.templ` `StaleCalendar`, the `p.Stale` loop). Google writes refuse a stale change with an ETag (`EventForm.Etag`). Its rank-1 "harden sync observability" is largely done already. |
| Week-view wishes counted as a gap | Month, week, day and agenda views exist (`internal/app/calendar.go` view switch, `internal/web/calendar.templ` view nav). |
| "Light/dark theme" listed, yet "no dark mode" and "rotating screensaver" counted against GoCasa | Per-device theme (`POST /device/theme`, `internal/app/app.go`). A no-repeat shuffled photo rotation (`internal/app/rotation.go`) from Immich albums, people and Memories (`internal/app/immich.go` `Memories`) or a Fallback folder (`internal/app/fallback.go`). |
| Split colours for multi-person events: not mentioned | Already built. An event with several Members is filled with diagonal stripes of every Member's colour (`internal/web/calendar.templ` `eventFill`, `repeating-linear-gradient(135deg …)`), and the time grid shows up to three avatars. |
| Tap a date to add an event: counted as a gap | Every month cell and agenda day has a "+" that opens a new event on that date (`internal/web/calendar.templ` `monthDay`, `calendarDay`, `newEventURL`). Saving returns to the event's own date (`internal/app/calendar.go` `saveEvent` redirects to `/calendar?date=…`). |
| Editing synced events and assigning people (Finks_02): counted as a sync gap (now **partly**: the reviewer doesn't say which calendars) | Google events in a writable calendar open the full event form and save back to Google (`POST /calendar/synced/{id}/edit`, `internal/app/app.go`). Any Google event can have GoCasa Members, and later syncs keep them (`internal/app/event_members.go`; `GLOSSARY.md` **Default Members**). iCal feed events remain read-only (`internal/web/settings.templ` FeedsSection). |
| Budgeting filed under "Event details & location" | A budgeting wish (Larron See). Out of scope for GoCasa; it is not a location request. |
| "Notifications & reminders: 9, No" | 2 of the 9 are complaints about **too many** notifications (daily update pop-ups; marketing pushes). GoCasa has neither: it has no notifications at all, and no telemetry (README). |

## 3. Corrected theme table

One primary theme per review. Counts are within this sample only.

| Theme | Reviews | gap | solved | partly | n/a | Verdict for GoCasa |
|---|---:|---:|---:|---:|---:|---|
| Subscriptions and paywalls | 14 | | 14 | | | **Selling point.** No subscription, no account |
| Phone, laptop and widget access | 14 | 8 | 5 | | 1 | Browser access: solved. Home-screen widgets and install: **gap** (§4.2) |
| Calendar entry, editing and navigation | 10 | | 10 | | | **Selling point** (§5) |
| Vendor bugs, crashes, Wi-Fi, hardware | 9 | | | | 9 | n/a |
| Meal planning and recipes | 9 | 8 | | | 1 | **Gap** (§4.3) |
| Reminders and change alerts | 8 | 8 | | | | **Gap** (§4.1): 7 reminders, 1 change alert |
| Accounts, logins, setup and privacy | 8 | | 3 | | 5 | Mostly n/a. No logins for the family is a selling point |
| Chores, routines and child access | 6 | 2 | 1 | 2 | 1 | Mixed. Sibling-chore guard **gap** (§4.7); steps inside a chore: gap, not ranked (§6) |
| Display look and customisation | 6 | | 3 | 2 | 1 | **Selling point** (dark mode, default view, holiday feeds, any orientation) |
| Photos and screensaver | 6 | 1 | 4 | 1 | | **Selling point**. No video (gap, not ranked) |
| Two-way sync with other calendars | 6 | 3 | | 3 | | Google: covered. Apple/iCloud and other providers: **gap** (§4.5) |
| Voice, AI and automation entry | 5 | 5 | | | | Gap, **poor fit** (§6) |
| Shopping-list drift (vendor bug) | 3 | | | | 3 | n/a. Mirrored lists treat Mealie as the source of truth |
| Multi-Member events and colours | 3 | | 3 | | | **Selling point** |
| Event location | 3 | | 3 | | | **Selling point**. Showing it is a tiny gap (§4.6) |
| Unwanted notifications and pop-ups | 2 | | 2 | | | **Selling point** |
| Event search | 2 | 2 | | | | **Gap** (§4.4). 4 reviews counting secondary asks |
| Out of scope (budgeting, store APIs) | 2 | | | | 2 | n/a |
| On-screen note | 1 | 1 | | | | **Gap** (§4.8). 2 reviews counting secondary asks |
| **Total** | **117** | **38** | **48** | **8** | **23** | |

**How this differs from the spreadsheet's 16 "Calendar sync & reliability" rows:**
- 6 are real sync complaints with other calendars: R35, R40, R44, R59, R84, R113.
- 1 is a multi-person Google event (R57), which GoCasa solves.
- The other 9 went to other themes: app slowness or crashes (R20, R28, R68, R77), Wi-Fi (R71, R108), an email-forwarding import that failed silently (R29), "Add From Image" (R94) and shopping-list drift (R17).

## 4. Ranked recommendations

| # | Gap | Evidence | GoCasa today | Effort | Fit |
|---|---|---|---|---|---|
| 1 | Event reminders, several per event, no nagging | 7 reminder reviews + 1 change alert, 3 products; 4 ask for several or custom alerts | None. Spec: "in-app display only in v1" | Wall: small–medium. Phones: medium–large | Wall: high. Phone push: needs a scope decision |
| 2 | Install on a phone, widget path | 8 widget reviews + 3 secondary mentions, 3 products (4 about calendar widgets); install itself is my inference | Responsive pages, `/calendar.ics`; no web manifest | Small (needs HTTPS) | High |
| 3 | Meal plan from Mealie on Today and the screensaver | 8 meal reviews, 3 products, 1 answered directly, 2 adjacent; 2 self-hosted sources | Mealie shopping lists only | Medium | High |
| 4 | Event search | 2 primary + 2 secondary, Hearth only | None | Small | High |
| 5 | Two-way Apple/iCloud (CalDAV) | 2 name Apple/iPhone; 2 broader; 1 iOS setup ask | iCal read-only in, ICS out | Large | High (pure-Go library exists) |
| 6 | Show location in the views | 3 asks for a feature that exists but can't be seen | Stored, synced, not displayed | Tiny | High |
| 7 | Child can't complete a sibling's chores | 1 direct (+1 "freeze the screen") | Device-wide unlock; can't tell siblings apart | Small–medium | Unclear: clashes with "only Admins have PINs" |
| 8 | An on-screen note | 1 primary + 1 secondary | None | Small | High, but #107 excluded parent messages |

### 4.1 Event reminders, several per event, with no nagging

**Evidence** (8 reviews: 7 want reminders, 4 of them several or custom alerts per event (R16, R25, R48, R62); R4 wants to be told when an event is added or changed, which reminders don't answer):
- "You get two notifications maximum. For some appointments we need a notification a day before, the morning of, and then at the time of the event." ([Lace Atkinson, Skylight, 2026-09-08][sky-ios])
- "being able to set multiple alerts for the same event" ([Potatospam7, Hearth, 2026-03-05][hearth])
- "I can't figure out how to get it to send me notifications before an event happens." ([Hysxc6, Cozyla, 2026-09-24][cozyla])
- "I would love to see reminders pushed to your phone for tasks or events." ([JJMatt82, Skylight, 2026-08-07][sky-ios])
- "a notification function that lets people know when an event has been scheduled, or edited." ([Jenkins330, Skylight, 2026-10-19][sky-ios])
- The flip side: "no way to turn off marketing messages; an app like this needs to use that notification privilege with care" ([yonatan_E, Skylight, 2026-07-28][sky-ios]), and daily update pop-ups "you have to acknowledge before using the calendar" ([Johnny Groovy, Skylight, 2026-09-28][sky-ios]).

**GoCasa today:** no reminders of any kind. No `VALARM` in the ICS export (`internal/app/ics.go`), no reminder fields in the Google write (`internal/app/google_write.go` `googleBody` sends summary, location, description and times only). The spec sets the scope: "Email/SMS reminders — in-app display only in v1" (`docs/skylite-go-spec.md` §2). (Its separate "No push notifications" line in §7.2 is about Google pushing changes to GoCasa, not reminders.)

**What partly works today, by inference (not tested):**
- Events GoCasa writes to a Google calendar should get that calendar's default reminders in Google's own apps, because GoCasa sends no `reminders` field.
- Events on local calendars reach a phone only through the ICS subscription, which has no alarms.

**The gap, in three steps that build on each other:**
1. **Per-event reminder offsets** stored with the event ("1 day before, at 8:00 the same morning, 15 min before"). Written to Google as `reminders.overrides` on Google calendars, and as `VALARM`s in `/calendar.ics`. **Small–medium.**
   - *Unverified:* whether iOS and Android keep alarms from a *subscribed* calendar. iOS has a "Remove Alerts" option on subscriptions.
2. **Upcoming reminders on the wall**: a banner on Today and the screensaver for the next due reminder. **Small.**
   - No phone involved, so it fits every constraint. It is the "in-app display" the spec already allows.
3. **Web Push to phones** that opted in. **Medium–large.**
   - Needs HTTPS, a service worker and the manifest from §4.2.
   - On iOS, push works only for a web app added to the Home Screen (*unverified* for current iOS).
   - This is new scope beyond the spec's "in-app display only".
   - In the Web Push protocol (RFC 8030), the server sends outbound to the browser vendor's push service, so GoCasa needn't be reachable from the internet to deliver. The phone only needs to have opened GoCasa once to subscribe.
   - But reminder text then passes through Apple's, Google's or Mozilla's push service, encrypted end to end per RFC 8291. That is a third-party relay for a "no cloud" app.

**No nagging, by design:** reminders only on events someone set them on, with no default reminders on every event. No marketing or "what's new" pop-ups, ever (GoCasa has neither today).

**Fit:** steps 1–2 fit fully. Step 3 needs a scope decision (open question 1). R4's "tell me when an event changes" would need the same phone channel; it's left out here.

### 4.2 Phone access: an installable web app, quick add and a widget path

**Evidence** (8 gap reviews, 3 products. 4 want a *calendar* widget (R1, R51, R81, R91); R47, R55 and R109 want calendar and tasks or the day; R13 wants a *task* widget, which nothing here answers. 3 more reviews filed under other themes also mention widgets: R42, R49, R53. None asks to install a web app; that is my inference of the nearest thing a web app can do):
- "The app needs a phone widget option to be able to look at the family calendar at a glance" ([Kailey Taylor, Skylight, 2025-03-24][sky-play])
- "please provide a widget that gives you a weekly view" ([JordanMarieT, Hearth, 2025-11-04][hearth])
- "Would love a widget option for a quick overview of the day without needing to open the app!" ([brandeella, Hearth, 2025-08-22][hearth])
- "the iOS widget only shows 3 events at a time, which defeats the point." ([LaNinaSopresa, Cozyla, 2026-10-17][cozyla])
- "Needs to have an Apple widget calendar for full functionality. Ideally would like to replace my apple calendar with cozyla" ([kt0299, Cozyla, 2026-08-27][cozyla])

5 more reviews in this theme are **already solved**: 3 ask for laptop or browser access, which GoCasa *is*, and 2 (R112, R114) for to-do and shopping lists the phone app hides, which every GoCasa page shows in any browser.
- "I really wish I could access with my laptop to add things" ([SDugGale10, Hearth, 2026-10-16][hearth])
- "we need the ability to be able to add from a web browser would make bulk adding and planning much better." ([NoNamesAvailiable1470, Cozyla, 2026-08-09][cozyla])
- Also R88 in the appendix.

**GoCasa today:**
- Every page is a responsive web page. The calendar header packs into one scrolling row on phones (`internal/web/calendar.templ`, the `max-[759px]` classes).
- `/calendar.ics` exports every calendar or a chosen set (`internal/app/app.go` `GET /calendar.ics`; `internal/app/ics.go` `getICS`), and the calendar menu shows the link (`internal/web/calendar.templ` `calendar-list`).
- There is **no web manifest or Home Screen icon**: `internal/web/home.templ` links only a favicon and the stylesheet, and `internal/web/static/` has no manifest.
- Remote (off-LAN) access is the homelab's job (`docs/skylite-go-spec.md` §2, README "Remote access").

**The gap:**
- **Installable web app** (small): a `manifest.webmanifest`, icons and `apple-touch-icon`, embedded like the other static files. GoCasa then opens from the Home Screen like an app. `docs/research/home-assistant-and-android.md` already recommends a PWA for Chromebooks.
- **A quick-add page for phones** (small–medium): one form for an event, to-do or list item. *My inference:* no review asks for this in those words. It is the nearest thing a web app can offer to "add without opening the app".
- **Widgets:** a web app can't put a native widget on an iPhone Home Screen. The honest path is to **tell people** that subscribing to `/calendar.ics` in the phone's own calendar shows GoCasa events in that calendar's widget. That works today, but only while the phone can reach GoCasa. A real widget is a native-app job, and native apps are a non-goal (`docs/skylite-go-spec.md` §2).

**Fit:** high. Static files in the binary, no new dependency. Installing needs HTTPS (`docs/research/home-assistant-and-android.md` §3), which GoCasa leaves to the reverse proxy (README "HTTPS").

### 4.3 Meal plans read from Mealie

**Evidence** (8 gap reviews, 3 products; plus self-hosted asks). A read-only meal view answers only R107 (calendar and meals hard to cross-reference) directly. R65 (integrate meal planning and grocery lists) and R58 (an archive of go-to meals to choose from while planning) sit next to it but are planning asks. The rest (R5, R61, planning then listing; R33, recipe categories; R90, dragging meals; R102, importing recipes) are planning and recipe work that would stay in Mealie. *Unverified* here how well Mealie itself does each.
- "It's tedious to use another app to plan my weekly menu and then add each grocery item individually to Hearth's list." ([aelbery, Hearth, 2025-08-11][hearth])
- "I wish there was a way to have an archive of your go-to meals and their corresponding recipes that you can look through and choose from when planning." ([ridelbi, Hearth, 2025-08-16][hearth])
- "I would like to be able to swap meals between dates on the calendar just by dragging them around." ([mascma, Cozyla, 2026-08-30][cozyla])
- "the calendar and meals to properly cross-reference each other is problematic from a navigation aspect" ([davidrit2, Cozyla, 2026-07-05][cozyla])
- Self-hosted: an open Home Assistant dashboard PR whose home screen shows "tonight's dinner" and whose meals are "a repeating weekly breakfast / lunch / dinner grid" ([my-skylight-calendar PR #1][ha-pr]). A paying DAKboard owner: "when your life is busy, it can be difficult to remember what you're supposed to be having for dinner." ([Sean O'Grady, 2022-08-15][ogrady])

**GoCasa today:**
- Mealie is an Integration for **shopping lists only**: `internal/app/mealie.go` calls `/api/households/shopping/lists` and `/api/households/shopping/items`.
- No meal-plan table, page or Today block (no `mealplan` anywhere in `internal/`).
- The spec keeps "richer Mealie items (foods/units/labels)" as a post-v1 option (`docs/skylite-go-spec.md` §2).

**The gap:** **read** Mealie's meal plan on the same Integration and show "Dinner tonight" on Today and the screensaver, and a week of meals on its own view. Keep planning, recipes, dragging and "add the ingredients to the list" in Mealie, which owns them. GoCasa's shopping lists already mirror whatever Mealie puts on the list.
- *Unverified:* Mealie's meal-plan endpoint and the token scope needed (I believe it is under `/api/households/mealplans`). Check it against Mealie's API before designing.

**Effort:** medium. A sync step, one table, a Today block and a page. It follows the existing Integration framework (`internal/app/integrations.go`).

**Fit:** high. Local SQLite mirror, nothing at request time (spec §3 principle 3). Households without Mealie lose nothing.

### 4.4 Event search

**Evidence** (2 primary, 2 secondary, all Hearth):
- "For the price, I expected more functionality, especially the ability to search for events, which should be a standard feature." ([Vsgfxhjsn, Hearth, 2024-09-21][hearth])
- "I would love a search function!" ([Mouse Griswold, Hearth, 2025-08-10][hearth])
- Also "needs a search function" ([CL0V3, 2026-02-04][hearth]) and "really needs a search function" ([Finks_02, 2025-08-14][hearth]).

**GoCasa today:** no search. There's no search route in `internal/app/app.go` (the only search is the weather place lookup, `internal/app/weather.go` `searchPlaces`). Navigation is by date only.

**The gap:** a search box on the calendar that matches title, location and description across stored events and lists the matches in agenda style with links to their dates.
- A `LIKE` query over `events` is enough for one household.
- Google events are kept only from 3 months back to 12 months ahead (`internal/app/google_sync.go` `syncWindow`), so search can't reach beyond that for them.

**Effort:** small. **Fit:** high. Evidence comes from one product only, so it ranks below the cross-product gaps despite the low effort.

### 4.5 Two-way Apple/iCloud sync (CalDAV)

**Evidence** (R35 and R84 name Apple or iPhone; R40 says "my phone calendar"; R44 says only "other colander's apps"; R85 is an iOS setup ask):
- "difficulties editing and deleting events from the calendar. This seems to be an issue with syncing with my Apple calendar" ([2347CAnnaC, Skylight, 2026-07-05][sky-ios])
- "Your directions for getting the app to connect with my phone calendar are outdated and have never worked. I have to manually enter everything onto my phone calendar as well as enter everything into the Skylight calendar" ([JH7508, Skylight, 2026-06-22][sky-ios])
- "The calendar seems to sync with my iPhone calendar, but only in one direction." ([alexdejesus62, Cozyla, 2026-09-27][cozyla])
- "can't get it to work with other colander's apps" ([Chicagofal, Hearth, 2026-09-22][hearth])
- "Needs better tutorial in Calendar sinking, especially with iOS devices" ([Raising2azkids, Cozyla, 2026-09-26][cozyla])

**GoCasa today:**
- Two-way Google (`internal/app/google_sync.go`, `internal/app/google_write.go`). That covers R59 and R113 when the calendar is Google, which neither review says (so both are **partly**): "it can delete your calendar events if you link it to your calendar, but can't add them" ([Broken Game Spotter, Cozyla, 2026-05-05][cozyla]).
- iCloud can come in only as a read-only iCal feed (a public iCloud calendar link), shown as "Read-only iCal or webcal calendars" (`internal/web/settings.templ` FeedsSection).
- GoCasa's events go out to Apple Calendar as an ICS subscription (`internal/app/ics.go`).
- The spec: "true CalDAV sync is a Phase 6 candidate (`emersion/go-webdav`)" (`docs/skylite-go-spec.md` §2). No issue tracks it.

**The gap:** a CalDAV Integration (iCloud with an app-specific password, Nextcloud, Fastmail), mirrored like Google: upstream first, Members kept locally.

**Effort:** large. Discovery, ETags and sync tokens, recurrence edge cases already solved once for Google (`internal/app/series_edit.go`), plus iCloud's quirks (*unverified*).

**Fit:** high. `emersion/go-webdav` is pure Go and from the same author as `emersion/go-ical`, already in `go.mod`.

The evidence is real but small, and the cost is the largest here. That's why it ranks below the cheaper wins.

### 4.6 Show event locations in the views

**Evidence:** 3 reviews ask for an event location or address:
- "there's still no way to add a location to an event. That seems like a pretty important oversight." ([Raclark, Hearth, 2024-07-27][hearth])
- "If I could enter the address in addition to notes, this would be 5 stars!" ([SC Mama x4, Cozyla, 2026-08-04][cozyla])
- R70 ([Plschosanotr, Hearth, 2024-10-09][hearth])

GoCasa already **stores** a location (§2), so these are solved. But it never **shows** it:
- `CalendarEvent`, the type every view, Today and the screensaver render from, has no Location field (`internal/web/calendar.templ`).
- iCal feed events have no page at all (`eventLink` renders them as plain text), so their location is invisible anywhere in GoCasa.

**The gap:** add the location under the time in agenda, day/week blocks, Today and the event dialog's read-only view. Optionally make it a maps link (open question 6).

**Effort:** tiny. **Fit:** high.

### 4.7 A child can't complete a sibling's chores

**Evidence** (1 direct; R106 wants the whole screen frozen, which a page can't do):
- "We do wish there was a screen lock so the littles wouldn't be able to mark off their siblings' chores" ([TLAS11, Skylight, 2026-06-25][sky-ios])
- "not being able to freeze the screen so the kids cant play on it." ([TroutKrewMama, Cozyla, 2026-07-15][cozyla])

**GoCasa today:**
- The chore Lock works per device, not per child. With it on, a locked device asks for an Admin's PIN before a completion (`internal/app/app.go`, `requireLock(web.LockChores, …)`). But "One unlock covers every Lock" (`internal/app/members.go` `requireLock`) until 5 minutes pass with no activity, someone taps "Lock now", or a Wall display enters the screensaver (`docs/skylite-go-spec.md` §9, Unlock lifetime).
- Either way, a completion is "credited to whoever taps their avatar (no PIN)" (`docs/skylite-go-spec.md` §8). So on an unlocked shared screen, nothing tells siblings apart.
- Only Admins have PINs (`GLOSSARY.md` **Admin**, **Lock**).
- The planned Member display (#112) shows one Member's page per device, which may cover the case of a child's own tablet.

**The gap:** stop one child completing another's chores on a shared Wall display without a parent unlocking it for them. Possible approaches: a kid PIN, completing only from the Member page or Member display, or a parent approving later.

**Effort:** small–medium. **Fit:** depends on a domain decision (open question 4), since any kid PIN contradicts "Only Admins have a PIN".

"Freezing the screen" is outside a web page's reach. Remote device control is covered in `docs/research/kiosk-satellite.md` §5.

### 4.8 An on-screen note

**Evidence** (1 primary, 1 secondary):
- "Can there be a 'note' section that you could do a live note for the person using it? … leave a 'post it note' virtually on the screen" ([phantomaudio72, Skylight, 2026-10-10][sky-ios])
- "a white board function for leaving notes" ([Addicted2dci, Hearth, 2025-08-11][hearth])

**GoCasa today:** none. To-dos have notes (`docs/skylite-go-spec.md` §6, `todos.notes`), but nothing shows free text on Today or the screensaver. Map #107 put "Messages from parents to a Member display" out of scope as "new stored data, a fresh effort if wanted".

**The gap:** one short Household note (with an optional end date) on Today and the screensaver, editable by anyone, or behind the calendar Lock.

**Effort:** small: one household setting, live-updated over the existing SSE kind `KindHousehold` (`internal/web/kind.go`). **Fit:** high.

The evidence is thin, so ask the household before building it.

## 5. Already solved: selling points for the README

These are things paying owners complain about or wish for, and GoCasa already does. The README doesn't mention most of them.

| Pain in the reviews | GoCasa | Example review |
|---|---|---|
| Subscription for photos, routines, "any useful feature" (14) | One install, no subscription or account (README; config needs no account: `internal/config/config.go`) | "Woke up one day … to a QR code that basically said, 'if you want to show pictures, you need an upgraded plan.'" ([Maxwellh, Skylight, 2026-08-06][sky-ios]) |
| Laptop and browser access (5) | It is a web app; any browser on the LAN (server-rendered pages, `internal/web/*.templ`; phone layout in `internal/web/calendar.templ`) | "the web app is essentially obsolete" ([SDugGale10, Hearth, 2026-10-16][hearth]) |
| Tap a day to add; edit events; months ahead; week view; event order (10) | "+" per day; full edit; Prev/Next by date; month/week/day/agenda; events sorted by time (`internal/app/events.go`, `slices.SortFunc`) | "the ability to tap a day to add an event for that day has been removed." ([RayO3D, Skylight, 2026-06-25][sky-ios]) |
| One event for several kids, split colours (3) | Many Members per event, striped in each colour (`internal/web/calendar.templ` `eventFill`) | "when you put multiple people in an event that the event would split the colors … Instead, it turns gray." ([MAdramma, Cozyla, 2026-07-21][cozyla]) |
| Edit synced events and assign people (R59), add events to a linked calendar (R113); partly: Google only | Google edits write back (`internal/app/google_write.go`); Members on any Google event (`internal/app/event_members.go`) | "you can't edit any of the events you sync from different calendars" ([Finks_02, Hearth, 2025-08-14][hearth]) |
| Event location (3) | Stored and synced (§4.6) | see §4.6 |
| Rotating screensaver, more photos, daily memories (4) | Immich albums, people and Memories (`internal/app/immich.go`), or a folder (`internal/app/fallback.go`); no-repeat shuffle (`internal/app/rotation.go`) | "we just wish the screensaver had an option to stream daily memories instead of starting at the first picture every time." ([Cozyla user, 2026-08-27][cozyla]) |
| Dark mode, default view, landscape, holidays (3 + 2 partly) | Per-device theme and Default view (`POST /device/theme`, `POST /device/default-view` in `internal/app/app.go`); a responsive layout that switches at 760 px wide rather than on orientation (the `min-[760px]` classes in `internal/web/today.templ` and `internal/web/calendar.templ`); holiday or birthday calendars as iCal feeds (`internal/app/ical.go`) or yearly events (`EventForm.Repeat` in `internal/web/calendar.templ`). Not: filling a date with a colour (R50), holidays listed automatically, or colours by event type rather than by Member or calendar (R79, `eventFill` in `internal/web/calendar.templ`) | "my eyes are begging for a dark mode option." ([ThatParksGuy, Cozyla, 2026-06-29][cozyla]) |
| Marketing pushes, forced update pop-ups (2) | No notification code anywhere in `internal/`; no telemetry (README) | see §4.1 |
| Separate logins, deleting profiles, forced photo-library access (3) | No family logins; Admins delete or archive Members (`POST /settings/members/{id}/delete` in `internal/app/app.go`); no phone app asking for permissions | "the fact I need to contact support to delete a profile is burdensome." ([rbe2, Hearth, 2025-11-03][hearth]) |
| "Up for grabs" chores; household maintenance without a person; ticking off yesterday's (1 + 2 partly) | Anyone chores (`GLOSSARY.md` **Anyone chore**). Partly: maintenance as Recurring to-dos in a per-room column, since every Chore needs a person assigned (`internal/app/chores.go` `choreForm`); yesterday's Round stays claimable until the next Round ends (`GLOSSARY.md` **Late**), older ones need an Adjustment | "options for all general maintenance things around the home … without having to create a 'person' to assign it to." ([SkitzyOne1, Cozyla, 2026-08-05][cozyla]) |
| Silent sync failures (no review in this sample asks directly) | "Sync failing (N in a row)" in Settings (`internal/web/settings.templ` `syncStatus`); stale banner on the calendar (`internal/web/calendar.templ` `StaleCalendar`) | — |

Partly solved, or not done:
- The screensaver shows **images only**, no videos (R24, [JBL Listener, Skylight, 2026-08-20][sky-ios]): a gap, not ranked (§6).
- Photos show on the screensaver, not behind the calendar ([OreoMME, Cozyla, 2026-08-10][cozyla]).

## 6. Not applicable, or not worth pursuing

- **Vendor bugs, crashes, Wi-Fi, touchscreens, logins** (9 + 5 rows). This includes R29, whose forwarded emails silently never became events. GoCasa has no email-to-event import at all, so that one is not a selling point. Each product's own reliability problems. The lesson for GoCasa is the one it already follows: render from local SQLite, so a dead upstream degrades sync and never the screen (`docs/skylite-go-spec.md` §3 principle 3).
- **Shopping-list drift** (3): "Keeps re-adding items that I've checked off and deleting other items that I've added." ([Mcblake91, Skylight, 2026-07-05][sky-ios]) A vendor bug. GoCasa's Mirrored lists treat Mealie as the source of truth with no offline queue (spec §3 principle 4).
- **Voice, Alexa, Shortcuts, AI "Add From Image"** (5). Each needs a cloud assistant or an AI service, or at least a documented write API.
  - *Inference, untested:* GoCasa's CSRF guard (`http.NewCrossOriginProtection`, `internal/app/app.go`) lets through requests that carry no browser headers. So an iOS Shortcut posting the `note` form field to `POST /lists/{id}/items` might already add a list item. That is undocumented and unsupported.
  - A documented JSON API is already planned in the spec's route table (`/api/*`, §8). That is the right home for automation if it's ever wanted.
- **Budgeting, grocery-store cart APIs, video messages for kids** (3). Outside a family calendar, or cloud-bound.
- **Steps inside one chore** ([JustVerasThoughtsAndOpinions, Skylight, 2026-09-21][sky-ios]): a gap on one review, not ranked. The planned Routines are different: they order whole daily Each chores under Morning, Afternoon and Evening (#109, #107; `GLOSSARY.md` **Routine**), with no steps inside a chore.
- **Videos on the screensaver** (R24): a gap on one review, not ranked. Immich photos are fetched as images only (`docs/skylite-go-spec.md` §10, `type:{eq:"IMAGE"}`).
- **Freezing the whole screen** (R106): not something a web page can do; remote device control is in `docs/research/kiosk-satellite.md` §5.
- **Remote control of the display from a phone; motion wake; face crops; display recovery.** Covered in `docs/research/kiosk-satellite.md`.

## 7. Open questions

1. **Reminders on phones: new scope?** The spec allows only "in-app display" for reminders in v1 (§2). Web Push would send reminder text, encrypted, through Apple's, Google's or Mozilla's push service. Is that acceptable as an opt-in, or do reminders stay on the wall, plus Google's and the phone calendar's own alarms (steps 1–2 of §4.1)? The same channel would decide whether "tell me when an event changes" (R4) is possible.
2. **Do phone calendars keep `VALARM`s from a subscribed ICS feed?** Test on iOS and Android before relying on step 1 for local calendars.
3. **Mealie's meal-plan API.** Confirm the endpoint, fields and token scope against a live Mealie v2/v3, and whether "tonight's dinner" should also come from a Mealie recipe's link.
4. **Child lock.** Kid PINs contradict "Only Admins have a PIN". Alternatives: completing only from the Member page or Member display (#112), or completions a parent confirms later. Which one?
5. **Search scope.** Should search cover to-dos and chores too, or only events? Google events are kept only 3 months back and 12 months ahead.
6. **Location as a link.** A maps link from the location (Apple Maps or Google Maps URL) is a link out of the house. Fine on a phone; on a Wall display it would leave the kiosk. Show it as text only on Wall displays?
7. **A better sample.** If the roadmap leans on this, a second pass should sample the *display's* reviews (Amazon or vendor sites) and self-hosted forums with a login, rather than the companion apps only.

## Sources

Primary review sources (each store's review list; find a review by its reviewer and date):

- Skylight, App Store: https://apps.apple.com/us/app/skylight-app/id1438779037?see-all=reviews
- Skylight, Google Play: https://play.google.com/store/apps/details?id=com.skylightframe.mobile
- Hearth Display, App Store: https://apps.apple.com/us/app/id6498717775?see-all=reviews
- Cozyla, App Store: https://apps.apple.com/us/app/id6504553395?see-all=reviews
- DAKboard, App Store: https://apps.apple.com/us/app/id1333473784?see-all=reviews

Context sources:

- [Sean O'Grady, "Why I set up a family dashboard"](https://sogrady.org/2022/08/15/why-i-set-up-a-family-dashboard/) (a paying DAKboard household, 2022)
- [my-skylight-calendar PR #1](https://github.com/speaker-builder-netizen/my-skylight-calendar/pull/1) (open Home Assistant dashboard PR)
- [HA Family Hub thread](https://community.home-assistant.io/t/ha-family-hub-a-ha-focused-alternative-to-skylight-nori-cozyla-etc/1025747) (title only; the forum blocked fetching)
- Second-hand Reddit quotes on subscriptions, via [RemindHer's Skylight review](https://remindher.app/skylight-calendar-review/) and [Hearth review](https://remindher.app/hearth-display-review/). Not counted.

GoCasa: the files cited inline, at `a709af3`.

## Appendix: every review re-themed

R*n* is the *n*th row of the spreadsheet's "Paid display feedback" sheet, in its order. "Spreadsheet theme" is its label; "My theme" and "Verdict" are this note's.

| # | Product | Reviewer, date | Spreadsheet theme | My theme | Verdict |
|---|---|---|---|---|---|
| R1 | Skylight Calendar | [Kailey Taylor][sky-play], 2025-03-24 | Mobile/web access & widgets | Phone, laptop and widget access | gap |
| R2 | Skylight Calendar | [Tori Christensen][sky-play], 2025-06-07 | Chores, routines & child access | Vendor bugs, crashes, Wi-Fi, hardware | n/a |
| R3 | Skylight Calendar | [Larron See][sky-play], 2026-11-19 | Event details & location | Out of scope (budgeting, store APIs) | n/a |
| R4 | Skylight Calendar | [Jenkins330][sky-ios], 2026-10-19 | Notifications & reminders | Reminders and change alerts | gap |
| R5 | Skylight Calendar | [novafang][sky-ios], 2026-10-18 | Meals, recipes & shopping | Meal planning and recipes | gap |
| R6 | Skylight Calendar | [SDAM1326][sky-ios], 2026-10-12 | Subscription & paywalls | Subscriptions and paywalls | solved |
| R7 | Skylight Calendar | [anthonyb176][sky-ios], 2026-10-11 | Account, setup & support | Accounts, logins, setup and privacy | n/a |
| R8 | Skylight Calendar | [phantomaudio72][sky-ios], 2026-10-10 | Calendar entry & navigation | On-screen note | gap |
| R9 | Skylight Calendar | [Johnny Groovy][sky-ios], 2026-09-28 | Notifications & reminders | Unwanted notifications and pop-ups | solved |
| R10 | Skylight Calendar | [Annoyed Gramma][sky-ios], 2026-09-28 | Subscription & paywalls | Subscriptions and paywalls | solved |
| R11 | Skylight Calendar | [Phb9][sky-ios], 2026-09-27 | Integrations & automation | Voice, AI and automation entry | gap |
| R12 | Skylight Calendar | [Diiulloodles][sky-ios], 2026-09-25 | Calendar entry & navigation | Calendar entry, editing and navigation | solved |
| R13 | Skylight Calendar | [Gamegirl1775][sky-ios], 2026-09-21 | Chores, routines & child access | Phone, laptop and widget access | gap |
| R14 | Skylight Calendar | [JustVerasThoughtsAndOpinions][sky-ios], 2026-09-21 | Chores, routines & child access | Chores, routines and child access | gap |
| R15 | Skylight Calendar | [hughouse][sky-ios], 2026-09-20 | Calendar entry & navigation | Display look and customisation | n/a |
| R16 | Skylight Calendar | [Lace Atkinson][sky-ios], 2026-09-08 | Notifications & reminders | Reminders and change alerts | gap |
| R17 | Skylight Calendar | [Purpl3keys][sky-ios], 2026-09-07 | Calendar sync & reliability | Shopping-list drift (vendor bug) | n/a |
| R18 | Skylight Calendar | [AppleseedMom][sky-ios], 2026-09-07 | Account, setup & support | Subscriptions and paywalls | solved |
| R19 | Skylight Calendar | [shellyrae][sky-ios], 2026-08-30 | Calendar entry & navigation | Calendar entry, editing and navigation | solved |
| R20 | Skylight Calendar | [Chrstn1022][sky-ios], 2026-08-29 | Calendar sync & reliability | Vendor bugs, crashes, Wi-Fi, hardware | n/a |
| R21 | Skylight Calendar | [margritz2000][sky-ios], 2026-08-24 | Chores, routines & child access | Chores, routines and child access | solved |
| R22 | Skylight Calendar | [Euodia][sky-ios], 2026-08-24 | Photo/media limits | Photos and screensaver | solved |
| R23 | Skylight Calendar | [Nocalmom7][sky-ios], 2026-08-21 | Subscription & paywalls | Subscriptions and paywalls | solved |
| R24 | Skylight Calendar | [JBL Listener][sky-ios], 2026-08-20 | Photo/media limits | Photos and screensaver | gap |
| R25 | Skylight Calendar | [KaylaTVan][sky-ios], 2026-08-08 | Notifications & reminders | Reminders and change alerts | gap |
| R26 | Skylight Calendar | [JJMatt82][sky-ios], 2026-08-07 | Notifications & reminders | Reminders and change alerts | gap |
| R27 | Skylight Calendar | [Maxwellh][sky-ios], 2026-08-06 | Subscription & paywalls | Subscriptions and paywalls | solved |
| R28 | Skylight Calendar | [prettyflyforanappguy][sky-ios], 2026-08-06 | Calendar sync & reliability | Vendor bugs, crashes, Wi-Fi, hardware | n/a |
| R29 | Skylight Calendar | [Mogan Toban][sky-ios], 2026-07-30 | Calendar sync & reliability | Vendor bugs, crashes, Wi-Fi, hardware | n/a |
| R30 | Skylight Calendar | [yonatan_E][sky-ios], 2026-07-28 | Notifications & reminders | Unwanted notifications and pop-ups | solved |
| R31 | Skylight Calendar | [jphotog][sky-ios], 2026-07-27 | Privacy & local access | Accounts, logins, setup and privacy | solved |
| R32 | Skylight Calendar | [Jcarlson12][sky-ios], 2026-07-25 | Notifications & reminders | Reminders and change alerts | gap |
| R33 | Skylight Calendar | [C.Stefanick][sky-ios], 2026-07-07 | Meals, recipes & shopping | Meal planning and recipes | gap |
| R34 | Skylight Calendar | [Easymac106][sky-ios], 2026-07-06 | Calendar entry & navigation | Calendar entry, editing and navigation | solved |
| R35 | Skylight Calendar | [2347CAnnaC][sky-ios], 2026-07-05 | Calendar sync & reliability | Two-way sync with other calendars | gap |
| R36 | Skylight Calendar | [Mcblake91][sky-ios], 2026-07-05 | Meals, recipes & shopping | Shopping-list drift (vendor bug) | n/a |
| R37 | Skylight Calendar | [bkrumsch][sky-ios], 2026-06-27 | Calendar entry & navigation | Calendar entry, editing and navigation | solved |
| R38 | Skylight Calendar | [RayO3D][sky-ios], 2026-06-25 | Calendar entry & navigation | Calendar entry, editing and navigation | solved |
| R39 | Skylight Calendar | [TLAS11][sky-ios], 2026-06-25 | Chores, routines & child access | Chores, routines and child access | gap |
| R40 | Skylight Calendar | [JH7508][sky-ios], 2026-06-22 | Calendar sync & reliability | Two-way sync with other calendars | gap |
| R41 | Hearth Display | [SDugGale10][hearth], 2026-10-16 | Mobile/web access & widgets | Phone, laptop and widget access | solved |
| R42 | Hearth Display | [balance12344][hearth], 2026-10-08 | Mobile/web access & widgets | Phone, laptop and widget access | n/a |
| R43 | Hearth Display | [War man 1882][hearth], 2026-09-22 | Subscription & paywalls | Subscriptions and paywalls | solved |
| R44 | Hearth Display | [Chicagofal][hearth], 2026-09-22 | Calendar sync & reliability | Two-way sync with other calendars | partly |
| R45 | Hearth Display | [Socially_ari][hearth], 2026-06-24 | Account, setup & support | Accounts, logins, setup and privacy | n/a |
| R46 | Hearth Display | [JBbur25][hearth], 2026-06-17 | Subscription & paywalls | Subscriptions and paywalls | solved |
| R47 | Hearth Display | [katroot09][hearth], 2026-05-17 | Mobile/web access & widgets | Phone, laptop and widget access | gap |
| R48 | Hearth Display | [Potatospam7][hearth], 2026-03-05 | Notifications & reminders | Reminders and change alerts | gap |
| R49 | Hearth Display | [CL0V3][hearth], 2026-02-04 | Integrations & automation | Voice, AI and automation entry | gap |
| R50 | Hearth Display | [hubby415][hearth], 2025-11-16 | Display customization & accessibility | Display look and customisation | partly |
| R51 | Hearth Display | [JordanMarieT][hearth], 2025-11-04 | Mobile/web access & widgets | Phone, laptop and widget access | gap |
| R52 | Hearth Display | [rbe2][hearth], 2025-11-03 | Account, setup & support | Accounts, logins, setup and privacy | solved |
| R53 | Hearth Display | [Liz151922][hearth], 2025-10-28 | Display customization & accessibility | Display look and customisation | solved |
| R54 | Hearth Display | [Aspencupcake][hearth], 2025-08-25 | Subscription & paywalls | Subscriptions and paywalls | solved |
| R55 | Hearth Display | [brandeella][hearth], 2025-08-22 | Mobile/web access & widgets | Phone, laptop and widget access | gap |
| R56 | Hearth Display | [KF2][hearth], 2025-08-18 | Subscription & paywalls | Subscriptions and paywalls | solved |
| R57 | Hearth Display | [sslaugh89][hearth], 2025-08-18 | Calendar sync & reliability | Multi-Member events and colours | solved |
| R58 | Hearth Display | [ridelbi][hearth], 2025-08-16 | Meals, recipes & shopping | Meal planning and recipes | gap |
| R59 | Hearth Display | [Finks_02][hearth], 2025-08-14 | Calendar sync & reliability | Two-way sync with other calendars | partly |
| R60 | Hearth Display | [Stephen_C82][hearth], 2025-08-14 | Display customization & accessibility | Photos and screensaver | solved |
| R61 | Hearth Display | [aelbery][hearth], 2025-08-11 | Meals, recipes & shopping | Meal planning and recipes | gap |
| R62 | Hearth Display | [Addicted2dci][hearth], 2025-08-11 | Meals, recipes & shopping | Reminders and change alerts | gap |
| R63 | Hearth Display | [figutspiner1000][hearth], 2025-08-10 | Display customization & accessibility | Vendor bugs, crashes, Wi-Fi, hardware | n/a |
| R64 | Hearth Display | [Mouse Griswold][hearth], 2025-08-10 | Calendar entry & navigation | Event search | gap |
| R65 | Hearth Display | [Disco_Pierre][hearth], 2025-08-08 | Meals, recipes & shopping | Meal planning and recipes | gap |
| R66 | Hearth Display | [Great family tool][hearth], 2025-08-08 | Integrations & automation | Voice, AI and automation entry | gap |
| R67 | Hearth Display | [sffoodie11][hearth], 2024-11-29 | Calendar entry & navigation | Multi-Member events and colours | solved |
| R68 | Hearth Display | [Roxy’s Mama][hearth], 2024-11-20 | Calendar sync & reliability | Vendor bugs, crashes, Wi-Fi, hardware | n/a |
| R69 | Hearth Display | [Tim45678uu][hearth], 2024-11-20 | Subscription & paywalls | Subscriptions and paywalls | solved |
| R70 | Hearth Display | [Plschosanotr][hearth], 2024-10-09 | Event details & location | Event location | solved |
| R71 | Hearth Display | [M_AT&T][hearth], 2024-10-07 | Calendar sync & reliability | Vendor bugs, crashes, Wi-Fi, hardware | n/a |
| R72 | Hearth Display | [abarisone][hearth], 2024-09-30 | Subscription & paywalls | Subscriptions and paywalls | solved |
| R73 | Hearth Display | [Vsgfxhjsn][hearth], 2024-09-21 | Calendar entry & navigation | Event search | gap |
| R74 | Hearth Display | [Wisco94747][hearth], 2024-09-16 | Display customization & accessibility | Display look and customisation | solved |
| R75 | Hearth Display | [Ktebbs][hearth], 2024-07-29 | Subscription & paywalls | Subscriptions and paywalls | solved |
| R76 | Hearth Display | [Raclark][hearth], 2024-07-27 | Event details & location | Event location | solved |
| R77 | Hearth Display | [Lyzzy B][hearth], 2024-07-18 | Calendar sync & reliability | Vendor bugs, crashes, Wi-Fi, hardware | n/a |
| R78 | Hearth Display | [JoannaS][hearth], 2024-06-30 | Subscription & paywalls | Subscriptions and paywalls | solved |
| R79 | Cozyla | [Stellar413][cozyla], 2026-10-22 | Display customization & accessibility | Display look and customisation | partly |
| R80 | Cozyla | [CO_Dad_John][cozyla], 2026-10-21 | Account, setup & support | Accounts, logins, setup and privacy | solved |
| R81 | Cozyla | [LaNinaSopresa][cozyla], 2026-10-17 | Mobile/web access & widgets | Phone, laptop and widget access | gap |
| R82 | Cozyla | [Field and fortress][cozyla], 2026-10-01 | Calendar entry & navigation | Calendar entry, editing and navigation | solved |
| R83 | Cozyla | [KC-OUT][cozyla], 2026-09-28 | Account, setup & support | Accounts, logins, setup and privacy | n/a |
| R84 | Cozyla | [alexdejesus62][cozyla], 2026-09-27 | Calendar sync & reliability | Two-way sync with other calendars | gap |
| R85 | Cozyla | [Raising2azkids][cozyla], 2026-09-26 | Account, setup & support | Accounts, logins, setup and privacy | n/a |
| R86 | Cozyla | [Praction3r][cozyla], 2026-09-25 | Calendar entry & navigation | Calendar entry, editing and navigation | solved |
| R87 | Cozyla | [Hysxc6][cozyla], 2026-09-24 | Notifications & reminders | Reminders and change alerts | gap |
| R88 | Cozyla | [KTBug613][cozyla], 2026-09-07 | Mobile/web access & widgets | Phone, laptop and widget access | solved |
| R89 | Cozyla | [Sh 67][cozyla], 2026-09-03 | Display customization & accessibility | Photos and screensaver | solved |
| R90 | Cozyla | [mascma][cozyla], 2026-08-30 | Meals, recipes & shopping | Meal planning and recipes | gap |
| R91 | Cozyla | [kt0299][cozyla], 2026-08-27 | Mobile/web access & widgets | Phone, laptop and widget access | gap |
| R92 | Cozyla | [Cozyla user][cozyla], 2026-08-27 | Display customization & accessibility | Photos and screensaver | solved |
| R93 | Cozyla | [OreoMME][cozyla], 2026-08-10 | Photo/media limits | Photos and screensaver | partly |
| R94 | Cozyla | [Nagoyagr][cozyla], 2026-08-09 | Calendar sync & reliability | Voice, AI and automation entry | gap |
| R95 | Cozyla | [Allnamesaretakencheeseandrice][cozyla], 2026-08-09 | Meals, recipes & shopping | Out of scope (budgeting, store APIs) | n/a |
| R96 | Cozyla | [NoNamesAvailiable1470][cozyla], 2026-08-09 | Mobile/web access & widgets | Phone, laptop and widget access | solved |
| R97 | Cozyla | [Blaine’s Mommy][cozyla], 2026-08-08 | Calendar entry & navigation | Calendar entry, editing and navigation | solved |
| R98 | Cozyla | [CCFITCC][cozyla], 2026-08-06 | Integrations & automation | Voice, AI and automation entry | gap |
| R99 | Cozyla | [SkitzyOne1][cozyla], 2026-08-05 | Chores, routines & child access | Chores, routines and child access | partly |
| R100 | Cozyla | [h iuuxebundw o edi][cozyla], 2026-08-05 | Chores, routines & child access | Chores, routines and child access | partly |
| R101 | Cozyla | [SC Mama x4][cozyla], 2026-08-04 | Event details & location | Event location | solved |
| R102 | Cozyla | [Court_910][cozyla], 2026-08-04 | Meals, recipes & shopping | Meal planning and recipes | gap |
| R103 | Cozyla | [MAdramma][cozyla], 2026-07-21 | Display customization & accessibility | Multi-Member events and colours | solved |
| R104 | Cozyla | [IveTriedEmAll][cozyla], 2026-07-16 | Subscription & paywalls | Subscriptions and paywalls | solved |
| R105 | Cozyla | [VicRum3][cozyla], 2026-07-15 | Calendar entry & navigation | Calendar entry, editing and navigation | solved |
| R106 | Cozyla | [TroutKrewMama][cozyla], 2026-07-15 | Chores, routines & child access | Chores, routines and child access | n/a |
| R107 | Cozyla | [davidrit2][cozyla], 2026-07-05 | Meals, recipes & shopping | Meal planning and recipes | gap |
| R108 | Cozyla | [cleogenia42][cozyla], 2026-07-02 | Calendar sync & reliability | Vendor bugs, crashes, Wi-Fi, hardware | n/a |
| R109 | Cozyla | [Anna antique][cozyla], 2026-07-01 | Mobile/web access & widgets | Phone, laptop and widget access | gap |
| R110 | Cozyla | [ThatParksGuy][cozyla], 2026-06-29 | Display customization & accessibility | Display look and customisation | solved |
| R111 | Cozyla | [Guitarbanae][cozyla], 2026-05-14 | Meals, recipes & shopping | Meal planning and recipes | n/a |
| R112 | Cozyla | [FjffjdjtjousSfhudg][cozyla], 2026-05-08 | Mobile/web access & widgets | Phone, laptop and widget access | solved |
| R113 | Cozyla | [Broken Game Spotter][cozyla], 2026-05-05 | Calendar sync & reliability | Two-way sync with other calendars | partly |
| R114 | Cozyla | [Alberto G-mez][cozyla], 2026-04-25 | Mobile/web access & widgets | Phone, laptop and widget access | solved |
| R115 | Cozyla | [Lucius Maximus Aurelius][cozyla], 2026-04-19 | Calendar entry & navigation | Calendar entry, editing and navigation | solved |
| R116 | Cozyla | [JKinkoph][cozyla], 2026-04-12 | Meals, recipes & shopping | Shopping-list drift (vendor bug) | n/a |
| R117 | DAKboard | [Jomarei][dakboard], 2024-04-25 | Account, setup & support | Accounts, logins, setup and privacy | n/a |

[sky-ios]: https://apps.apple.com/us/app/skylight-app/id1438779037?see-all=reviews
[sky-play]: https://play.google.com/store/apps/details?id=com.skylightframe.mobile
[hearth]: https://apps.apple.com/us/app/id6498717775?see-all=reviews
[cozyla]: https://apps.apple.com/us/app/id6504553395?see-all=reviews
[dakboard]: https://apps.apple.com/us/app/id1333473784?see-all=reviews
[ogrady]: https://sogrady.org/2022/08/15/why-i-set-up-a-family-dashboard/
[ha-pr]: https://github.com/speaker-builder-netizen/my-skylight-calendar/pull/1
