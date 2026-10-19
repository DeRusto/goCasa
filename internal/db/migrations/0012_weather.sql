-- Weather (spec §6, §10). household_settings is one row; other tickets add
-- their own columns to it, so it is created only if none has yet.
CREATE TABLE IF NOT EXISTS household_settings (id INTEGER PRIMARY KEY CHECK (id = 1));
INSERT OR IGNORE INTO household_settings (id) VALUES (1);
ALTER TABLE household_settings ADD COLUMN units TEXT NOT NULL DEFAULT 'metric' CHECK (units IN ('metric', 'imperial'));
ALTER TABLE household_settings ADD COLUMN weather_lat REAL; -- NULL: no weather
ALTER TABLE household_settings ADD COLUMN weather_lon REAL;

-- Open-Meteo is synced like any integration; with no location it fetches nothing.
INSERT INTO integrations (type, name, next_sync_at) VALUES ('open_meteo', 'Open-Meteo', 0);

-- The last forecast fetched, for where and in which units: pages show it only
-- while it matches the household settings.
CREATE TABLE weather_cache (
	id         INTEGER PRIMARY KEY CHECK (id = 1),
	lat        REAL NOT NULL,
	lon        REAL NOT NULL,
	units      TEXT NOT NULL,
	fetched_at INTEGER NOT NULL,
	body       TEXT NOT NULL -- Open-Meteo's JSON answer
);
