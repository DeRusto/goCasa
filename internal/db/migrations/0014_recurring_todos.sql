-- Recurring to-dos (spec §8): each copy carries the chain's rule; a completed
-- copy points at the next copy its completion created.
ALTER TABLE todos ADD COLUMN repeat_rule TEXT; -- preset RRULE, NULL unless it repeats
ALTER TABLE todos ADD COLUMN repeat_from_completion INTEGER NOT NULL DEFAULT 0;
ALTER TABLE todos ADD COLUMN next_id INTEGER REFERENCES todos (id) ON DELETE SET NULL;
