-- Provisional ticks are visible only on the Member page and earn no points.
CREATE TABLE routine_ticks (
    member_id INTEGER NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    chore_id INTEGER NOT NULL REFERENCES chores(id) ON DELETE CASCADE,
    round_start TEXT NOT NULL,
    PRIMARY KEY (member_id, chore_id, round_start)
);

-- Completing a Round from any page supersedes that Member's provisional tick.
CREATE TRIGGER clear_routine_tick AFTER INSERT ON chore_completions
BEGIN
    DELETE FROM routine_ticks
    WHERE member_id = NEW.member_id AND chore_id = NEW.chore_id AND round_start = NEW.round_start;
END;
