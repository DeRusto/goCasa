# GoCasa

A self-hosted family dashboard for one household: shared calendar, to-dos, shopping lists, chores and a photo screensaver on a wall display.

## Language

### People

**Household**:
The one family a GoCasa instance serves.
_Avoid_: Family, tenant, group

**Member**:
Anyone or anything in the Household that events and chores can belong to; a person or a pet. A Member may or may not be able to operate the app.
_Avoid_: User, person, account, profile

**Admin**:
A Member allowed to change Settings: integrations, Members and household settings. Only Admins have a PIN, and an Admin's PIN unlocks everything.
_Avoid_: Owner, parent, superuser

**Lock**:
An optional household rule that makes one kind of action (changing the calendar, completing chores, completing to-dos) need an Admin's PIN. Viewing is never locked.
_Avoid_: PIN protection, household PIN

**Archived Member**:
A Member who has left the Household. Hidden from filters, pickers and chore boards, but still credited with their past events, completions and points.
_Avoid_: Deleted member, inactive user

### Calendar

**Household event**:
An event that belongs to no Member, and so shows for everyone whatever the filter.
_Avoid_: Shared event, family event, unassigned event

**Series**:
A recurring event: one definition with a repeat rule that produces many Occurrences.
_Avoid_: Recurring master, recurring event group

**Occurrence**:
One dated instance of a Series.
_Avoid_: Instance, recurrence

**One-off edit**:
A change to, or deletion of, a single Occurrence that leaves the rest of its Series alone.
_Avoid_: Exception, override, detached occurrence

**Split**:
Ending a Series just before a chosen Occurrence and starting a new, independent Series from it.
_Avoid_: Fork, branch, truncate

**Google account**:
One Google login an Admin has connected to GoCasa. Its calendars sync into GoCasa; connecting another login adds another Google account.
_Avoid_: Integration, connection, Google sync

**Reconnect needed**:
The state of a Google account whose access Google has withdrawn. It stops syncing, keeps showing its last known events, and only an Admin reconnecting the same login fixes it.
_Avoid_: Expired, disconnected, auth error

### To-dos

**Recurring to-do**:
A to-do that creates its next copy when completed; only one copy is open at a time.
_Avoid_: To-do series, repeating task, to-do occurrence

**Repeat from schedule**:
A Recurring to-do whose next due date follows its rule, skipping any dates already missed.
_Avoid_: Fixed, strict

**Repeat from completion**:
A Recurring to-do whose next due date is counted from the day it was completed.
_Avoid_: Floating, relaxed

### Chores

**Chore**:
A job that comes back on a schedule and earns points for whoever does it. Each round is done or lapses; it is never owed afterwards, unlike a to-do.
_Avoid_: Task, job, recurring to-do

**Round**:
One period of a Chore (a day, a week) in which it can be done once per Member.
_Avoid_: Occurrence, instance

**Each chore**:
A Chore that every assigned Member does separately, each earning its points.

**Anyone chore**:
A Chore that the first assigned Member to do it completes for the Round.
_Avoid_: Shared chore

**Late**:
A Round that ended undone but can still be claimed, until the next Round ends.
_Avoid_: Overdue

**Missed**:
A Round that can no longer be claimed. It leaves the chore board.
_Avoid_: Expired, skipped

**Reward**:
Something a Member can spend points on, with a fixed points cost.
_Avoid_: Prize, treat, store item

**Redemption**:
A Member's request to spend points on a Reward. Nothing is spent until an Admin approves it.
_Avoid_: Purchase, claim

**Adjustment**:
Points an Admin adds or removes by hand, with a reason. The only way a balance can go below zero.
_Avoid_: Bonus, penalty, correction

### Shopping

**Mirrored list**:
A shopping list that lives in Mealie and that GoCasa shows and changes. When Mealie is unavailable, it cannot be changed.
_Avoid_: Synced list, linked list, Mealie list

### Dashboard

**Today**:
The at-a-glance page for the whole Household: today's and tomorrow's events, today's Chores, to-dos due today and the weather.
_Avoid_: Home, dashboard, overview

**Default view**:
The page a device opens on and a Wall display returns to when it wakes. Each device can set its own; otherwise it uses the Household's.
_Avoid_: Home page, start page, landing view

### Screensaver

**Wall display**:
A device switched into always-on dashboard mode. Only a Wall display goes to the screensaver when idle or uses Night mode and camera wake; any other device just browses.
_Avoid_: Kiosk, display, screen, tablet

**Night mode**:
The Household's set hours when a Wall display shows a black screen, or a dim clock, instead of the screensaver.
_Avoid_: Sleep, do not disturb, off hours

**Fallback folder**:
A folder of the Household's own images that the screensaver shows only when Immich has no photos to show.
_Avoid_: Local photos, default images, backup folder
