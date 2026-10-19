-- Google write-back (spec §7.2): the etag GoCasa last saw, sent as If-Match.
ALTER TABLE events ADD COLUMN etag TEXT;
