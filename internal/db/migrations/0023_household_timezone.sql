-- The Household's timezone (spec §5, IANA name); '' falls back to the TZ the
-- server runs with.
ALTER TABLE household_settings ADD COLUMN timezone TEXT NOT NULL DEFAULT '';