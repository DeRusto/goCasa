-- Google sync (spec §7.2). Reconnect needed stops an account syncing.
ALTER TABLE integrations ADD COLUMN status TEXT NOT NULL DEFAULT 'ok' CHECK (status IN ('ok', 'reconnect_needed'));
-- A Google instance's recurringEventId: deleting a whole series may cancel only its id.
ALTER TABLE events ADD COLUMN external_series_id TEXT;
-- Set by a Member change made in GoCasa: syncs leave the event's Members alone.
-- On the event, not event_members, so an override to a Household event (no rows) holds too.
ALTER TABLE events ADD COLUMN local_override INTEGER NOT NULL DEFAULT 0 CHECK (local_override IN (0, 1));
UPDATE integrations SET sync_interval_seconds = 120, next_sync_at = coalesce(next_sync_at, 0) WHERE type = 'google_calendar';
