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
A Member allowed to change Settings: integrations, Members and household settings.
_Avoid_: Owner, parent, superuser

**Archived Member**:
A Member who has left the Household. Hidden from filters, pickers and chore boards, but still credited with their past events, completions and points.
_Avoid_: Deleted member, inactive user

### Calendar

**Household event**:
An event that belongs to no Member, and so shows for everyone whatever the filter.
_Avoid_: Shared event, family event, unassigned event
