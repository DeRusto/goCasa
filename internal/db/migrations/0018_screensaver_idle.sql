-- How long a Wall display waits with no input before its screensaver (spec §6, §8).
ALTER TABLE household_settings ADD COLUMN screensaver_idle_seconds INTEGER NOT NULL DEFAULT 300 CHECK (screensaver_idle_seconds > 0);
