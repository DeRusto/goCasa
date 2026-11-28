-- Routines (Spec: Member page): a daily or chosen-weekdays Each chore can be
-- in one of three Routines, at a position the Routine builder sets for the
-- whole Household. The slot times, minutes after midnight, only pick the
-- current Routine; they never make a Chore Late or Missed.
ALTER TABLE chores ADD COLUMN routine TEXT CHECK (routine IS NULL OR (routine IN ('morning', 'afternoon', 'evening')
	AND kind = 'each' AND schedule IN ('daily', 'weekdays') AND NOT archived));
ALTER TABLE chores ADD COLUMN routine_position INTEGER CHECK ((routine IS NULL) = (routine_position IS NULL));
ALTER TABLE household_settings ADD COLUMN morning_end INTEGER NOT NULL DEFAULT 720 CHECK (morning_end BETWEEN 1 AND 1439);
ALTER TABLE household_settings ADD COLUMN afternoon_end INTEGER NOT NULL DEFAULT 1020 CHECK (afternoon_end BETWEEN 1 AND 1439 AND afternoon_end > morning_end);
