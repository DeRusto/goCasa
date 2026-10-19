-- Default view, clock and week start (spec §6, §8). household_settings is one
-- row; other tickets add their own columns to it.
CREATE TABLE IF NOT EXISTS household_settings (id INTEGER PRIMARY KEY CHECK (id = 1));
INSERT OR IGNORE INTO household_settings (id) VALUES (1);
ALTER TABLE household_settings ADD COLUMN default_view TEXT NOT NULL DEFAULT 'today'; -- a name in defaultViews
ALTER TABLE household_settings ADD COLUMN clock_24h INTEGER NOT NULL DEFAULT 1 CHECK (clock_24h IN (0, 1));
ALTER TABLE household_settings ADD COLUMN week_start INTEGER NOT NULL DEFAULT 0 CHECK (week_start BETWEEN 0 AND 6); -- 0 = Sunday
