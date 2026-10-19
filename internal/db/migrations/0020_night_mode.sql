-- Night mode: between night_start and night_end (hours 0-23, household time) a
-- Wall display shows black or a dim clock instead of the screensaver (spec §8).
ALTER TABLE household_settings ADD COLUMN night_mode TEXT NOT NULL DEFAULT 'off' CHECK (night_mode IN ('off', 'black', 'dim_clock'));
ALTER TABLE household_settings ADD COLUMN night_start INTEGER NOT NULL DEFAULT 22 CHECK (night_start BETWEEN 0 AND 23);
ALTER TABLE household_settings ADD COLUMN night_end INTEGER NOT NULL DEFAULT 6 CHECK (night_end BETWEEN 0 AND 23);
