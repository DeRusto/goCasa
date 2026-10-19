-- Locks (spec §9): each off by default, changed only by an Admin.
-- household_settings is one row; other tickets add their own columns to it.
CREATE TABLE IF NOT EXISTS household_settings (id INTEGER PRIMARY KEY CHECK (id = 1));
INSERT OR IGNORE INTO household_settings (id) VALUES (1);
ALTER TABLE household_settings ADD COLUMN lock_calendar INTEGER NOT NULL DEFAULT 0 CHECK (lock_calendar IN (0, 1));
ALTER TABLE household_settings ADD COLUMN lock_todos INTEGER NOT NULL DEFAULT 0 CHECK (lock_todos IN (0, 1));
