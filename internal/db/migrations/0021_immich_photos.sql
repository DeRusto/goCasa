-- Immich photos on the screensaver (spec §6, §10). photos_cache is the
-- rotation list, replaced on each refresh; cache_path names a file in
-- DATA_DIR/photo-cache holding the proxied bytes, NULL until fetched.
ALTER TABLE household_settings ADD COLUMN seconds_per_photo INTEGER NOT NULL DEFAULT 30 CHECK (seconds_per_photo > 0);
CREATE TABLE photos_cache (
	id INTEGER PRIMARY KEY AUTOINCREMENT, -- never reused: browsers cache a photo by its id
	source TEXT NOT NULL CHECK (source IN ('immich', 'folder')),
	external_id_or_path TEXT NOT NULL,
	taken_at INTEGER,
	people_json TEXT NOT NULL DEFAULT '[]',
	cache_path TEXT,
	cache_bytes INTEGER NOT NULL DEFAULT 0,
	last_used_at INTEGER,
	UNIQUE (source, external_id_or_path)
);
