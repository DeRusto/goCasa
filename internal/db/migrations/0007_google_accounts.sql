-- Google accounts (spec §6, §7.2). One Google account is one integration,
-- known by its OpenID sub; its calendars are source_calendars with Google's id.
ALTER TABLE integrations ADD COLUMN account_sub TEXT;
CREATE UNIQUE INDEX integrations_account ON integrations (type, account_sub) WHERE account_sub IS NOT NULL;
ALTER TABLE source_calendars ADD COLUMN external_id TEXT;
ALTER TABLE source_calendars ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1));
CREATE UNIQUE INDEX source_calendars_external ON source_calendars (integration_id, external_id) WHERE external_id IS NOT NULL;

-- Default Members for events synced from a calendar.
CREATE TABLE source_calendar_members (
	calendar_id INTEGER NOT NULL REFERENCES source_calendars (id) ON DELETE CASCADE,
	member_id   INTEGER NOT NULL REFERENCES members (id) ON DELETE CASCADE, -- a default is not history
	PRIMARY KEY (calendar_id, member_id)
);
