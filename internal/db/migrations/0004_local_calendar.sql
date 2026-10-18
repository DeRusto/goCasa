-- Local calendars and single events (spec §6). The integration framework adds
-- its sync columns to integrations later; the built-in local integration is id 1.
CREATE TABLE integrations (
	id   INTEGER PRIMARY KEY AUTOINCREMENT,
	type TEXT NOT NULL,
	name TEXT NOT NULL
);
INSERT INTO integrations (id, type, name) VALUES (1, 'local', 'GoCasa');

CREATE TABLE source_calendars (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	integration_id INTEGER NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
	name           TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 50),
	color          TEXT NOT NULL CHECK (color GLOB '#[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]')
);

-- A timed event has UTC instants and its IANA zone; an all-day event has
-- dates only, never converted through a zone. Both ends are exclusive.
CREATE TABLE events (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	calendar_id      INTEGER NOT NULL REFERENCES source_calendars (id) ON DELETE CASCADE,
	title            TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
	description      TEXT NOT NULL DEFAULT '' CHECK (length(description) <= 5000),
	location         TEXT NOT NULL DEFAULT '' CHECK (length(location) <= 200),
	all_day          INTEGER NOT NULL CHECK (all_day IN (0, 1)),
	start_utc        INTEGER,
	end_utc          INTEGER,
	start_tzid       TEXT,
	all_day_date     TEXT, -- YYYY-MM-DD, first day
	all_day_end_date TEXT, -- YYYY-MM-DD, the day after the last
	updated_at       INTEGER NOT NULL,
	CHECK (CASE WHEN all_day
		THEN start_utc IS NULL AND end_utc IS NULL AND start_tzid IS NULL AND all_day_date < all_day_end_date
		ELSE all_day_date IS NULL AND all_day_end_date IS NULL AND start_tzid IS NOT NULL AND start_utc < end_utc END)
);
CREATE INDEX events_time ON events (start_utc, end_utc);
CREATE INDEX events_dates ON events (all_day_date, all_day_end_date);

-- No rows: a Household event. member_id has no ON DELETE action, so a Member
-- with events is archived, not deleted.
CREATE TABLE event_members (
	event_id  INTEGER NOT NULL REFERENCES events (id) ON DELETE CASCADE,
	member_id INTEGER NOT NULL REFERENCES members (id),
	PRIMARY KEY (event_id, member_id)
);
CREATE INDEX event_members_member ON event_members (member_id);
