# rrule-go vs Google recurrence, and Google's representation of exceptions in incremental sync

Research for issue #4. Question: does `teambition/rrule-go` expand ordinal BYDAY, COUNT/UNTIL, EXDATE/RDATE and DST-crossing series correctly, and how does Google Calendar API v3 `Events.list` + `syncToken` deliver edited occurrences, cancelled occurrences and changes to the master? Does that map onto spec §6's exception-row model?

Sources are primary: rrule-go source at `master` (v1.8.2, last commit 2023-04-01), RFC 5545, Go `time` docs, Google Calendar API v3 reference and guides. Statements not backed by a cited source are marked **unverified**.

## Short answer

- **rrule-go is good enough for Google series that start at normal times.** It builds every occurrence with `time.Date(...)` in the DTSTART's `time.Location`, so wall-clock time is kept across DST in both directions. Ordinal BYDAY (`2TU`, `-1FR`) uses dateutil's per-month algorithm, so it is correct for MONTHLY and YEARLY. COUNT, UNTIL, RDATE and EXDATE are all supported. EXDATEs match on the instant (`time.Equal`), not on the location.
- **Two DST edge cases are not RFC-exact.** Both only affect occurrences that fall in the 01:00–03:00 transition hour.
  1. A *non-existent* local time (spring-forward gap) is normalised forward by Go's `time.Date` (02:30 becomes 03:30). RFC 5545 §3.3.10 says such an instance MUST be ignored and not counted. We don't know what Google does here (**unverified**).
  2. An *ambiguous* local time (fall-back) is resolved by Go to "one of the two zones … not guaranteed which". RFC 5545 §3.3.5 says use the first one.
  The spec's existing golden-file plan (§14 risk 1) should include one 02:30 weekly series and one 01:30 weekly series to compare against Google.
- **Google's sync model maps cleanly onto §6.** An edited occurrence arrives as its own event with its own `id`, plus `recurringEventId` (the master's id) and `originalStartTime`. A cancelled occurrence arrives as `status: "cancelled"` with only `id`, `recurringEventId` and `originalStartTime` guaranteed. A deleted master arrives as `status: "cancelled"` with only `id` guaranteed. That is exactly "exception row keyed by original start". The schema needs a few fixes (see "Mapping onto spec §6").
- **rrule-go is unmaintained.** No commits since 2023-04, and RFC-5545 PRs (e.g. #66 for HOURLY across DST, #67 for UNTIL) have been open for a long time. Plan to vendor or fork it if a bug turns up.

## 1. rrule-go behaviour (source: https://github.com/teambition/rrule-go)

### Timezone / DST

- In `rrule.go`, the time set, the year start and each generated occurrence are built with `time.Date(..., r.dtstart.Location())` / `timeTemp.Location()`. So expansion runs in DTSTART's zone, on local wall-clock fields. https://github.com/teambition/rrule-go/blob/master/rrule.go
- The result: a weekly 09:00 `America/New_York` series stays at 09:00 local across both transitions, and the UTC instant moves by an hour. This is the RFC/Google behaviour. Google requires `start.timeZone` for recurring events and documents it as "the time zone in which the recurrence is expanded". https://developers.google.com/workspace/calendar/api/v3/reference/events
- Gap and overlap handling is delegated to Go's `time.Date`. Its docs say it "returns a time that is correct in one of the two zones involved in the transition, but it does not guarantee which", and out-of-range values "will be normalized". https://pkg.go.dev/time#Date
  - RFC 5545 §3.3.5: an ambiguous local time means the first occurrence. A non-existent one is interpreted with the offset before the gap. https://www.rfc-editor.org/rfc/rfc5545#section-3.3.5
  - RFC 5545 §3.3.10, on recurrence instances with a non-existent local time: "Such recurrence instances MUST be ignored and MUST NOT be counted as part of the recurrence set." https://www.rfc-editor.org/rfc/rfc5545#section-3.3.10
  - So for a 02:30 daily series on spring-forward day, rrule-go yields 03:30. A strict RFC implementation yields nothing that day. Google's behaviour is **unverified**.
- Known issues: #63 (open) covers HOURLY across DST producing duplicate timestamps. A fix exists in PR #66, which is unmerged. Google's UI cannot create HOURLY rules, so it doesn't matter for GoCasa. #59 is the same problem reported for HOURLY. https://github.com/teambition/rrule-go/issues/63, https://github.com/teambition/rrule-go/issues/59
- If DTSTART is passed in UTC, expansion is in UTC and DST is ignored entirely. This is the most likely integration bug. **Always construct DTSTART in `time.LoadLocation(event.start.timeZone)`.**

### Ordinal BYDAY

- `buildRRule` keeps an ordinal weekday (`n != 0`) in `bynweekday` only when `freq <= MONTHLY`. Otherwise the ordinal is dropped. `rebuild` then picks the n-th weekday from the month range for MONTHLY (or YEARLY with BYMONTH) or the year range for YEARLY. Negative `n` counts from the end (`i = last + (n+1)*7`). This is the dateutil algorithm. https://github.com/teambition/rrule-go/blob/master/rrule.go
- RFC 5545 forbids a numeric BYDAY outside MONTHLY/YEARLY ("MUST NOT be specified with a numeric value when the FREQ rule part is not set to MONTHLY or YEARLY"). So dropping it for WEEKLY is harmless.
- BYSETPOS is also implemented.

### COUNT vs UNTIL

- COUNT is decremented only after an occurrence passes the `>= dtstart` check, and `0` means unlimited. UNTIL is inclusive (`res.After(r.until)` stops the series) and is truncated to seconds. https://github.com/teambition/rrule-go/blob/master/rrule.go
- RFC 5545: UNTIL and COUNT "MUST NOT occur in the same 'recur'". When DTSTART has a TZID, UNTIL "MUST be specified as a date with UTC time" (the `Z` form).
- Parser (`str.go`): a 15-char UNTIL without `Z` is parsed in the passed `loc`, and the `Z` form is parsed as UTC. An 8-char date UNTIL is parsed as midnight in `loc`. https://github.com/teambition/rrule-go/blob/master/str.go. Issue #67 (open) complains that UNTIL is "always UTC" for floating DTSTART. That case does not apply to Google, which always gives a TZID.
- Caveat: a date-only `UNTIL=20261231` on a *timed* series parsed in `loc` becomes 2026-12-31 00:00. That would drop the last day's occurrence. **Unverified** whether Google emits date-only UNTIL for timed events. Add a golden case, or normalise date-only UNTIL to end-of-day.

### RDATE / EXDATE

- `RRuleSet.RDate` / `ExDate` store values truncated to seconds. Exclusion is `!dt.Equal(exlist[0].dt)`, i.e. instant comparison, so the location doesn't matter. https://github.com/teambition/rrule-go/blob/master/rruleset.go
- `StrSliceToRRuleSetInLoc` handles `DTSTART;TZID=…` (which then becomes the default zone), `RDATE`/`EXDATE` with `TZID=`, `VALUE=DATE-TIME` and `VALUE=DATE`. `VALUE=PERIOD` is unsupported. The non-`InLoc` variants hard-code UTC. https://github.com/teambition/rrule-go/blob/master/str.go
- Google's `recurrence[]` contains "RRULE, EXRULE, RDATE and EXDATE lines … DTSTART and DTEND lines are not allowed". So GoCasa must synthesise `DTSTART;TZID=<start.timeZone>:<local start>`, prepend it, and call `StrSliceToRRuleSetInLoc`. EXRULE is deprecated in RFC 5545. rrule-go's `str.go` parser does not list it (**unverified** whether Google still emits it; treat it as an unsupported-series error).

## 2. Google Calendar API v3: recurrence in sync

Sources: Events resource https://developers.google.com/workspace/calendar/api/v3/reference/events, Events.list https://developers.google.com/workspace/calendar/api/v3/reference/events/list, sync guide https://developers.google.com/workspace/calendar/api/guides/sync, recurring events guide https://developers.google.com/workspace/calendar/api/guides/recurringevents.

| Change upstream | What `Events.list?syncToken=…` (singleEvents=false) returns |
|---|---|
| Series created/edited ("all events") | The master event: own `id`, `recurrence[]`, `start{dateTime,timeZone}`. The whole resource is sent again, not a diff. |
| One occurrence edited | A separate event with its own `id`, `recurringEventId` = master id, and `originalStartTime` (immutable; "uniquely identifies the instance within the recurring event series even if the instance was moved"). It has no `recurrence` field. |
| One occurrence deleted | `status: "cancelled"` exception. "Cancelled exceptions of an uncancelled recurring event indicate that this instance should no longer be presented to the user" and "are only guaranteed to have values for the id, recurringEventId and originalStartTime fields populated." |
| Whole series / single event deleted | `status: "cancelled"`. "Deleted events are only guaranteed to have the id field populated." |
| "This and following" | Not documented. Observed behaviour: the master gets an UNTIL and a new master is created (often id `<oldid>_R<yyyymmddThhmmss>`) (**unverified**). Either way it arrives as two ordinary master updates, which the model already handles. |

Sync-mechanics facts that matter:

- With `syncToken`, "All events deleted since the previous list request will always be in the result set", and `showDeleted=false` is not allowed.
- `syncToken` cannot be combined with `iCalUID, orderBy, privateExtendedProperty, q, sharedExtendedProperty, timeMin, timeMax, updatedMin`. Incremental sync is therefore unbounded in time: you get every changed master or exception, wherever it falls.
- `nextSyncToken` "is present only on the very last page". Persist it only after all pages are applied (in one transaction, per spec §7.1 rule 4).
- An expired token returns 410 GONE, and the client "should clear its storage and perform a full synchronization". This matches spec §7.
- The default order is "unspecified, stable". **An exception can arrive before its master** in the same sync.
- `singleEvents` is *not* on the syncToken-incompatible list. But the §14 escape hatch ("expand from Google's SingleEvents listing") still cannot use `timeMin`/`timeMax` together with syncToken. It would either sync unbounded instances or need windowed full re-lists. This is a cost to weigh if the escape hatch is ever used.

## 3. Mapping onto spec §6

The model fits: master row (`rrule`, `exdates_json`, `rdates_json`) plus exception rows (`recurring_master_id` + `original_start_utc`), with expansion skipping originals that have an exception. The gaps to fix in the spec:

1. **`original_start_utc` is missing from the `events` column list.** It is only mentioned in the `event_exceptions` line. Add it, and add a unique index on `(recurring_master_id, original_start_utc)`. Google's `originalStartTime` converts directly: use `dateTime` for a UTC instant, or `date` for all-day.
2. **Cancelled exceptions are rows with `status = cancelled`.** They carry no title or time, so the title/start/end columns must be nullable or filled from the master. Expansion: an exception row with `status=cancelled` suppresses that occurrence. Any other status replaces it.
3. **Master linkage by external id.** Google gives `recurringEventId` (the upstream id), and exceptions may arrive before the master. Either store `recurring_master_external_id` and resolve it on read, or apply a page set in two passes (masters first). One transaction per sync, which the spec already requires, makes the two-pass option straightforward.
4. **Deletes keyed by id only.** A cancelled event with no `recurringEventId` is a delete of a master or single event. Delete by `external_id` and cascade to its exception rows.
5. **Two exclusion mechanisms coexist.** Honour both master `EXDATE` (from `recurrence[]`, kept in `exdates_json`) and cancelled exception rows. Imported or ICS-origin series may use EXDATE.
6. **Instant matching.** Compare `original_start_utc` with rrule-go output as UTC instants, truncated to seconds. rrule-go already truncates to seconds and uses `Equal`.
7. **Orphaned exceptions after a master edit.** If "all events" changes the time or rule, existing exceptions' `originalStartTime` may no longer match any generated occurrence. What Google does with them (re-sends them as cancelled, keeps them, or rewrites them) is **unverified**. Pick a rule: show edited orphans as standalone, or drop them. Test it in the Phase 2 acceptance run.

## Throwaway DST check

Not run: no Go toolchain on this machine. A minimal check for later (scratch only, not project code):

```go
loc, _ := time.LoadLocation("America/New_York")
set, _ := rrule.StrSliceToRRuleSetInLoc([]string{
    "DTSTART;TZID=America/New_York:20261013T090000",
    "RRULE:FREQ=MONTHLY;BYDAY=2TU;COUNT=6",
    "EXDATE;TZID=America/New_York:20261208T090000",
}, loc)
for _, t := range set.All() { fmt.Println(t, t.UTC()) }
// expect 09:00 local every 2nd Tuesday, UTC hour 13 before Nov 1 and 14 after, Dec 8 absent
// repeat with DTSTART at 02:30 on 20260308 (gap) and 01:30 on 20261101 (overlap), FREQ=DAILY
```
