-- Fallback folder (spec §10): the Household's own images in DATA_DIR/photos,
-- listed in photos_cache with source 'folder', shown only when Immich has
-- nothing to show and this is on.
ALTER TABLE household_settings ADD COLUMN fallback_folder_enabled INTEGER NOT NULL DEFAULT 1 CHECK (fallback_folder_enabled IN (0, 1));
