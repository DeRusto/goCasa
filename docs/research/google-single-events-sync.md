# Research: syncing Google calendars with `singleEvents=true`

Researched 2026-10-17. Spec context: `docs/skylite-go-spec.md` §7.2. The spec currently says "Google returns recurrence masters, expansion is ours". The new decision is to store Google's expanded occurrences instead, write edits to Google first, and only change local rows through sync.

This builds on `docs/research/rrule-go-and-google-recurrence.md` (branch `research/rrule-go-and-google-recurrence`). That note already covers the cancelled/edited exception shapes, the `syncToken` exclusion list, the 410 handling, the fact that `nextSyncToken` arrives only on the last page, and the fact that an exception can arrive before its master. None of that is repeated here except where `singleEvents` changes the answer.

Sources are primary only: the Google Calendar API v3 reference and guides. Claims that no source supports are marked **unverified**. Short quotes are verbatim.

## Short answer

- **Yes, `singleEvents=true` works with `syncToken`.** It is not in the list of parameters that `syncToken` rejects, and Google's own sync guide uses `singleEvents=true&syncToken=…` in an example URL. `timeMin`/`timeMax` *are* rejected. So incremental syncs are unbounded in time even when the initial import was windowed.
- **No documented expansion horizon.** Without `timeMax`, Google says only that it doesn't filter by start time. How far a never-ending series is expanded, and whether a token sync ever sends instances that drift into a window later, is **unverified**. So treat the window as GoCasa's job: filter on apply, and refresh with a periodic windowed full re-sync.
- **A master change under `singleEvents` is undocumented.** Logically every expanded instance changes. Expect up to one row per instance within Google's (unknown) horizon (**unverified**).
- **Conditional writes are documented:** send `If-Match: <etag>` on update/delete. A mismatch returns **412 `conditionNotMet`**. Instances are ordinary event resources with their own `id` and `etag`. That If-Match works on an instance id is a reasonable inference, but **unverified** by an explicit statement.
- **Quota is a non-issue for polling every 2 minutes**: 720 requests per calendar per day, against 600 per user per minute and 1,000,000 per project per day. Google does call polling an anti-pattern and prefers push. Push needs a public HTTPS endpoint with a valid certificate, which a self-hosted LAN box usually lacks.

---

## 1. `singleEvents` + `syncToken`, and the disallowed parameters

- `singleEvents`: "Whether to expand recurring events into instances and only return single one-off events and instances of recurring events, but not the underlying recurring events themselves." https://developers.google.com/workspace/calendar/api/v3/reference/events/list
- `syncToken` cannot be used with `iCalUID`, `orderBy`, `privateExtendedProperty`, `q`, `sharedExtendedProperty`, `timeMin`, `timeMax`, `updatedMin`. `showDeleted=false` is also disallowed: deletions "will always be in the result set". The reference adds "All other query parameters should be the same as for the initial synchronization to avoid undefined behavior." https://developers.google.com/workspace/calendar/api/v3/reference/events/list
- `singleEvents` is therefore allowed. It *must* be sent on every request, initial and incremental, because the sync guide says "Each list request should use the same set of query parameters, including the initial request". A disallowed parameter gets a `400`. https://developers.google.com/workspace/calendar/api/guides/sync
- The sync guide's own example is `GET /calendars/primary/events?maxResults=10&singleEvents=true&syncToken=…`. https://developers.google.com/workspace/calendar/api/guides/sync
- `orderBy=startTime` needs `singleEvents=true` but is disallowed with `syncToken`. So don't use it in a sync loop. https://developers.google.com/workspace/calendar/api/v3/reference/events/list
- Note: some third-party write-ups claim `singleEvents` is incompatible with `syncToken`. The primary reference above does not say so.

## 2. Never-ending series, windows, and rolling the window

- `timeMax`: "Upper bound (exclusive) for an event's start time to filter by … The default is not to filter by start time." `timeMin` is a lower bound (exclusive) on an event's *end* time. Both need an RFC 3339 offset. https://developers.google.com/workspace/calendar/api/v3/reference/events/list
- **Horizon or cap without `timeMax`: not documented.** The list and instances references give no expansion limit, only page sizes (default 250, max 2500). https://developers.google.com/workspace/calendar/api/v3/reference/events/list, https://developers.google.com/workspace/calendar/api/v3/reference/events/instances. Google must stop *somewhere* for an infinite RRULE. Where it stops is **unverified**, so measure it (see the probe below).
- **A windowed initial import is officially supported.** Google's sync sample sets `timeMin` to one year ago on the full sync only, with the comment that sync tokens "aren't compatible with" most filters. https://developers.google.com/workspace/calendar/api/guides/sync
- **What incremental sync does outside the window.** The token request can't carry `timeMin`/`timeMax`, so it returns changed items wherever they fall. GoCasa must expect, and either store or drop, instances outside its window. Whether the token "remembers" the initial window is not documented (**unverified**).
- **Rolling window.** Nothing documented says an *unchanged* instance that enters the window as time passes (for example week 53 of a weekly series after a +12-month import) will be sent by an incremental sync. So assume it won't (**unverified**). The only documented way to pull it in is a new full sync with a fresh `timeMin`/`timeMax`. That sync also yields a fresh `nextSyncToken` on its last page. https://developers.google.com/workspace/calendar/api/guides/sync
- Google's quota guide warns against everyone doing a full sync "at midnight". Randomise the daily run and jitter intervals by about ±25%. https://developers.google.com/workspace/calendar/api/guides/quota

## 3. Master change under incremental `singleEvents` sync

- Not documented. With `singleEvents=true`, the master is never returned ("but not the underlying recurring events themselves"). https://developers.google.com/workspace/calendar/api/v3/reference/events/list. A series-wide time change must therefore show up as changed instances. The likely behaviour is that every instance within Google's expansion horizon comes back with new `start`/`end` and the same `id`/`originalStartTime` (**unverified**).
- Rough volume for a weekly never-ending series: one item per week up to the horizon. That is about 52 per year of horizon, so 1 page at the default 250 until the horizon exceeds about 4.8 years. Raise `maxResults` (max 2500) to keep it to one request. The horizon is **unverified**, so the number is too.
- Related, and documented: the recurring guide warns against editing instances one by one for series-wide changes, because it "creates lots of exceptions" and sends many change notifications. https://developers.google.com/workspace/calendar/api/guides/recurringevents
- Also **unverified**: whether a master change re-sends previously *edited* or *cancelled* instances (orphaned exceptions; see the prior note §3 item 7).

## 4. Cancelled and edited instances in `singleEvents` results

- Every occurrence has its own `id`, and all share the series `iCalUID`: "all occurrences of one event have different `id`s while they all share the same `iCalUID`s". https://developers.google.com/workspace/calendar/api/v3/reference/events
- **Every instance, edited or not,** carries `recurringEventId` ("the `id` of the recurring event to which this instance belongs", immutable) and `originalStartTime` ("uniquely identifies the instance within the recurring event series even if the instance was moved", immutable). It has no `recurrence` field ("omitted for single events or instances of recurring events"). https://developers.google.com/workspace/calendar/api/v3/reference/events, https://developers.google.com/workspace/calendar/api/guides/recurringevents
- **Edited instance:** it appears as a normal instance at its new `start`/`end`, with the same `id`, `recurringEventId` and `originalStartTime`. Nothing marks it as an exception. Same `id` before and after an edit is **unverified** (implied by the instance-update flow, which PUTs to the instance's id). https://developers.google.com/workspace/calendar/api/guides/recurringevents
- **Cancelled instance:** `status: "cancelled"`. Cancelled exceptions "are only guaranteed to have values for the `id`, `recurringEventId` and `originalStartTime` fields". A deleted master or single event guarantees only `id`. https://developers.google.com/workspace/calendar/api/v3/reference/events
- **Full sync** (no token): `showDeleted` defaults to false. Cancelled instances are included in that case only "if `showDeleted` and `singleEvents` are both False". So with `singleEvents=true`, a cancelled occurrence simply doesn't appear. **Incremental sync** always includes deletions, so cancelled instances arrive as above. https://developers.google.com/workspace/calendar/api/v3/reference/events/list
- **Deleting a whole series:** under `singleEvents`, whether the token sync returns one cancelled master id (which GoCasa has no row for) or a cancelled item per instance is **unverified**. Handle both: on a cancelled `id`, delete the row with that id *and* rows whose `recurring_event_id` equals it.

## 5. Conditional writes (`If-Match` / 412)

- "Every resource has a version field that changes every time the resource changes — the `etag` field." To update or delete conditionally, send "an `If-Match` header that contains the value of the etag from the previous retrieval". Otherwise "you will get a 412 (Precondition failed)". Inserts don't support conditional modification. https://developers.google.com/workspace/calendar/api/guides/version-resources
- 412 reason `conditionNotMet`: "The etag supplied in the If-match header no longer corresponds to the current etag of the resource." Action: "Re-fetch the entity and re-apply the changes." https://developers.google.com/workspace/calendar/api/guides/errors
- `Events.update` says to "perform a `get` followed by an `update` using etags to ensure atomicity". https://developers.google.com/workspace/calendar/api/v3/reference/events/update. `Events.patch` doesn't mention etags. It states "each patch request consumes three quota units; prefer using a `get` followed by an `update`". https://developers.google.com/workspace/calendar/api/v3/reference/events/patch. If-Match on patch is **unverified** but covered by the general guide's "update" wording.
- **Instances:** an instance is an event resource (it has `id` and `etag`), and the documented single-instance edit is "retrieve the instance" then PUT to the instance URL. https://developers.google.com/workspace/calendar/api/guides/recurringevents. So `If-Match` with the instance's etag should apply. No page states that explicitly (**unverified**). An instance's etag does not protect the master. Series edits need the master's own etag from `Events.get`.
- Conditional reads: `If-None-Match` returns 304 when the event is unchanged. Some changes don't move etags (for example read-only `calendarList` fields). https://developers.google.com/workspace/calendar/api/guides/version-resources

## 6. Getting the master for "this and following" / "all events"

- Take `recurringEventId` from the stored instance and call `Events.get(calendarId, recurringEventId)`. `get` "Returns an event based on its Google Calendar ID". Its `timeZone` parameter defaults to the calendar's zone. https://developers.google.com/workspace/calendar/api/v3/reference/events/get. The master carries `recurrence[]` (RRULE/EXRULE/RDATE/EXDATE, never DTSTART/DTEND) and `start.timeZone`. https://developers.google.com/workspace/calendar/api/v3/reference/events
- "All events": `update` the master with `If-Match: <master etag>`. Gotcha: the master's `start`/`end` is the *first* occurrence. To move the time, change only the time of day on the master's own date. Never copy the instance's date into the master (inference from the resource model; **unverified** as Google guidance).
- "This and following": there is **no API operation** for it, and the recurring guide only says don't do it by editing instances individually. https://developers.google.com/workspace/calendar/api/guides/recurringevents. The client must (a) update the master's RRULE with `UNTIL` just before the chosen instance's `originalStartTime`, and (b) insert a new series starting there. Steps (a) and (b) are not atomic, and insert has no If-Match. Google's own UI behaviour (new master id `<old>_R<timestamp>`) is **unverified**, as in the prior note.
- Gotchas:
  - `recurringEventId` may point to a master created by an earlier split. It's still a normal event; `get` works on it.
  - What happens to existing edited/cancelled instances after a master time or rule change is **unverified**. Re-sync afterwards and trust what comes back.
  - An instance whose master is cancelled is gone too. A `get` on a deleted id may return 410 `deleted` ("no further action is necessary"). https://developers.google.com/workspace/calendar/api/guides/errors

## 7. Quota for a 2-minute poll per calendar

- Limits: 10,000 requests/min per project, **600 requests/min per user per project**, 1,000,000/day per project. The daily figure is where charges start ("planned to incur charges … later in 2026"), not a hard stop. Standard use is free. Over-limit requests get 403 or 429 `usageLimits`/`rateLimitExceeded`; use truncated exponential backoff with up to 1,000 ms jitter, capped at 32–64 s. https://developers.google.com/workspace/calendar/api/guides/quota, https://developers.google.com/workspace/calendar/api/guides/errors
- Cost: 1 request per calendar per 2 minutes is 30/hour, **720/day per calendar**. A household with 8 calendars is about 5,800/day and 4/min. That is under 1% of the per-user minute limit, and about 0.6% of the daily project threshold even if each sync paginates a few pages. A nightly windowed re-import adds a few pages per calendar.
- Writes: `patch` costs 3 units, while `get` + `update` costs 2 and is the documented etag path. https://developers.google.com/workspace/calendar/api/v3/reference/events/patch
- Google calls polling every calendar an anti-pattern and recommends push notifications. https://developers.google.com/workspace/calendar/api/guides/quota. But push needs a public HTTPS receiver with a valid, hostname-matching certificate (not self-signed). Notifications carry no body (you still call list), and channels don't renew automatically. https://developers.google.com/workspace/calendar/api/guides/push. For a self-hosted LAN box, polling stays the practical choice. Optional: jitter the 2-minute interval by ±25% per the quota guide.

## Probe to settle the unverified bits (throwaway, not project code)

Against a test calendar, with `singleEvents=true`:
1. Create a weekly series with no end. Full-list with no `timeMin`/`timeMax` and count instances or the last `start`. That gives the horizon.
2. Save `nextSyncToken`, change the master's time via `Events.update`, then token-sync. Count the items, and check whether a previously edited instance and a cancelled instance are re-sent.
3. Do a full sync with `timeMax = now+30d`, save the token, wait for (or simulate with a short series) an instance crossing the window, and token-sync. Does it appear?
4. Delete the series and token-sync. Is it one cancelled master id or N cancelled instances?
5. PUT an instance with a stale `If-Match`. Expect 412.

## Implications for GoCasa

- **Spec §7.2 wording:** change it to "`Events.List` with `singleEvents=true` on every request (full and incremental), `syncToken` for incremental; expansion is Google's". rrule-go is then not needed for Google calendars (still relevant for iCal, §7.3).
- **Row key:** the Google instance `id`. Store `recurring_event_id`, `original_start` and `etag` alongside, for write-back and series edits. A cancelled `id` deletes that row and any rows with `recurring_event_id = id`. Exception rows and `exdates_json` aren't needed for Google rows.
- **Storage window:** keep the spec's **−3 months … +12 months**. On every apply (full or incremental), **drop items whose instance falls outside the window** and keep everything else. This bounds the database regardless of Google's horizon and of the unbounded token results.
- **Re-import strategy:** a **daily windowed full re-sync per calendar** (`singleEvents=true`, `timeMin=now−3mo`, `timeMax=now+12mo`, no token), at a randomised time. Apply all pages in one transaction, delete local rows in the window that weren't returned, and store the new `nextSyncToken`. This rolls the window forward a day at a time and repairs any drift. Also do it on 410. Incremental token syncs run every 2 minutes in between.
- **Writes:** `get` then `update` with `If-Match`. On 412, re-fetch, show the user the current version, and don't overwrite. "All events" edits fetch the master by `recurringEventId`. "This and following" is two calls (truncate RRULE, insert new series) with no atomicity. Consider leaving it out of Phase 2b.
- **Run the probe** before Phase 2 acceptance. If the horizon turns out to be short, or master changes return huge batches, the nightly re-sync already covers it. Only `maxResults=2500` matters for cost.
