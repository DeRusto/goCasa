-- Integration framework and iCal feeds (spec §5, §6, §7.3). next_sync_at NULL
-- never syncs, as for the built-in local integration.
ALTER TABLE integrations ADD COLUMN config_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE integrations ADD COLUMN credentials_enc BLOB; -- AES-256-GCM, nonce first (see seal)
ALTER TABLE integrations ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1));
ALTER TABLE integrations ADD COLUMN sync_interval_seconds INTEGER NOT NULL DEFAULT 900;
ALTER TABLE integrations ADD COLUMN last_sync_at INTEGER;
ALTER TABLE integrations ADD COLUMN last_success_at INTEGER;
ALTER TABLE integrations ADD COLUMN consecutive_failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE integrations ADD COLUMN next_sync_at INTEGER;
ALTER TABLE integrations ADD COLUMN last_error TEXT NOT NULL DEFAULT '';
ALTER TABLE integrations ADD COLUMN sync_cursor TEXT NOT NULL DEFAULT ''; -- the integration's own, e.g. an iCal feed's ETag
ALTER TABLE source_calendars ADD COLUMN read_only INTEGER NOT NULL DEFAULT 0 CHECK (read_only IN (0, 1));
-- A synced event's id upstream, e.g. its iCal UID; NULL for local events and One-off edit rows.
ALTER TABLE events ADD COLUMN external_id TEXT;
CREATE UNIQUE INDEX events_external ON events (calendar_id, external_id) WHERE external_id IS NOT NULL;
