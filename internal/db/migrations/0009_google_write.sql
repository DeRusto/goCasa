-- Google write-back (spec §7.2): the etag GoCasa last saw, sent as If-Match.
ALTER TABLE events ADD COLUMN etag TEXT;
-- A full re-import of every Google account fills it for events already synced.
UPDATE integrations SET sync_cursor = '' WHERE type = 'google_calendar';
