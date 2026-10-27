-- The screensaver's photo fit, captions and slow zoom (Household settings),
-- and what photos_cache needs for Photo pairs and captions: each photo's
-- upright size (0 when unknown) and where it was taken. Immich re-lists now
-- to fill them in; the Fallback folder rescans on start anyway.
ALTER TABLE household_settings ADD COLUMN photo_fit TEXT NOT NULL DEFAULT 'fill' CHECK (photo_fit IN ('fill', 'blur'));
ALTER TABLE household_settings ADD COLUMN photo_captions INTEGER NOT NULL DEFAULT 1 CHECK (photo_captions IN (0, 1));
ALTER TABLE household_settings ADD COLUMN photo_zoom INTEGER NOT NULL DEFAULT 1 CHECK (photo_zoom IN (0, 1));
ALTER TABLE photos_cache ADD COLUMN width INTEGER NOT NULL DEFAULT 0;
ALTER TABLE photos_cache ADD COLUMN height INTEGER NOT NULL DEFAULT 0;
ALTER TABLE photos_cache ADD COLUMN place TEXT NOT NULL DEFAULT '';
UPDATE integrations SET next_sync_at = 0 WHERE type = 'immich';
