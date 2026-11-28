-- Provisional ticks are visible only on the Member page and earn no points.
CREATE TABLE routine_ticks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    member_id INTEGER NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    chore_id INTEGER NOT NULL REFERENCES chores(id) ON DELETE CASCADE,
    round_start TEXT NOT NULL,
    expires_on TEXT NOT NULL,
    UNIQUE (member_id, chore_id, round_start)
);
CREATE INDEX routine_ticks_by_expiry ON routine_ticks(expires_on);
CREATE INDEX routine_ticks_by_chore ON routine_ticks(chore_id);

-- Completing a Round from any page supersedes that Member's provisional tick.
CREATE TRIGGER clear_completed_routine_tick AFTER INSERT ON chore_completions
BEGIN
    DELETE FROM routine_ticks
    WHERE member_id = NEW.member_id AND chore_id = NEW.chore_id AND round_start = NEW.round_start;
END;

-- Changing a Chore's Routine or schedule invalidates its provisional ticks.
-- Reordering a Chore within a Routine keeps them.
CREATE TRIGGER clear_changed_chore_routine_ticks AFTER UPDATE OF routine, schedule, days, archived ON chores
WHEN OLD.routine IS NOT NEW.routine OR OLD.schedule <> NEW.schedule OR OLD.days <> NEW.days OR NEW.archived
BEGIN
    DELETE FROM routine_ticks WHERE chore_id = NEW.id;
END;

CREATE TRIGGER clear_unavailable_member_routine_ticks AFTER UPDATE OF archived, pet ON members
WHEN NEW.archived OR NEW.pet
BEGIN
    DELETE FROM routine_ticks WHERE member_id = NEW.id;
END;
