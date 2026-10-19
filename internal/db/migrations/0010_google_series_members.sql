-- Members picked for a Google Series (All events, This and following, a Split
-- or a create), so its occurrences that sync later get them too, not the
-- calendar's defaults. from_key is the first Occurrence's start key, NULL for all.
CREATE TABLE google_series_members (
	calendar_id INTEGER NOT NULL REFERENCES source_calendars (id) ON DELETE CASCADE,
	series_id   TEXT NOT NULL,
	from_key    INTEGER,
	members     TEXT NOT NULL -- a JSON array of member ids
);
CREATE INDEX google_series_members_series ON google_series_members (calendar_id, series_id);
