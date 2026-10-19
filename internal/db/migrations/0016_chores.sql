-- Chores and Rounds (spec §8). Points balances, Rewards and Adjustments come later.
ALTER TABLE household_settings ADD COLUMN lock_chores INTEGER NOT NULL DEFAULT 0 CHECK (lock_chores IN (0, 1));
-- Pets are Members but can't be Admins or be assigned Chores.
ALTER TABLE members ADD COLUMN pet INTEGER NOT NULL DEFAULT 0 CHECK (pet IN (0, 1));

CREATE TABLE chores (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
	schedule   TEXT NOT NULL CHECK (schedule IN ('daily', 'weekdays', 'weekly', 'monthly')),
	days       TEXT NOT NULL DEFAULT '', -- chosen weekdays, e.g. 'MO,WE'; only for 'weekdays'
	kind       TEXT NOT NULL DEFAULT 'each' CHECK (kind IN ('each', 'anyone')),
	points     INTEGER NOT NULL CHECK (points BETWEEN 0 AND 10000),
	archived   INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1)),
	created_at INTEGER NOT NULL
);

CREATE TABLE chore_assignees (
	chore_id  INTEGER NOT NULL REFERENCES chores (id),
	member_id INTEGER NOT NULL REFERENCES members (id) ON DELETE CASCADE, -- an assignment isn't history
	PRIMARY KEY (chore_id, member_id)
);

-- Each completion keeps the points it earned, so later changes to a Chore
-- only affect future Rounds. An Anyone chore's one-per-Round is checked on insert.
CREATE TABLE chore_completions (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	chore_id       INTEGER NOT NULL REFERENCES chores (id),
	member_id      INTEGER NOT NULL REFERENCES members (id),
	round_start    TEXT NOT NULL, -- YYYY-MM-DD
	completed_at   INTEGER NOT NULL,
	points_awarded INTEGER NOT NULL
);
CREATE UNIQUE INDEX chore_completions_round ON chore_completions (chore_id, member_id, round_start);
