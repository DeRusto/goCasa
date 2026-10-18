-- Members (spec §6): avatar, archiving and wrong-PIN blocking.
ALTER TABLE members ADD COLUMN avatar BLOB; -- JPEG, 256 px square; NULL = initials on colour
ALTER TABLE members ADD COLUMN archived INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1));
ALTER TABLE members ADD COLUMN pin_failures INTEGER NOT NULL DEFAULT 0; -- wrong PINs in a row, incl. ones being checked
ALTER TABLE members ADD COLUMN pin_blocked_until INTEGER; -- unix seconds
