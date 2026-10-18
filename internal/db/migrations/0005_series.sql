-- Local Series and One-off edits (spec §6, §7.1). A master row holds the rule
-- and its first Occurrence; a One-off edit row is keyed by its Series and the
-- Occurrence's original start (UTC seconds; for all-day, the date's UTC midnight).
ALTER TABLE events ADD COLUMN rrule TEXT; -- RRULE value; NULL unless a master
ALTER TABLE events ADD COLUMN exdates_json TEXT NOT NULL DEFAULT '[]'; -- wall times in the event's zone, or dates
ALTER TABLE events ADD COLUMN rdates_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE events ADD COLUMN recurring_master_id INTEGER REFERENCES events (id) ON DELETE CASCADE
	CHECK (recurring_master_id IS NULL OR rrule IS NULL);
ALTER TABLE events ADD COLUMN original_start_utc INTEGER
	CHECK ((original_start_utc IS NULL) = (recurring_master_id IS NULL));
-- A cancelled row is a deleted Occurrence; it keeps the times it had so the
-- event CHECKs still hold.
ALTER TABLE events ADD COLUMN status TEXT NOT NULL DEFAULT 'confirmed' CHECK (status IN ('confirmed', 'cancelled'));
CREATE UNIQUE INDEX events_one_off ON events (recurring_master_id, original_start_utc);
