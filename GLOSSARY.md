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
