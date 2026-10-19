-- Points, Rewards, Redemptions and Adjustments (spec §8). Rewards are never
-- edited, so an approved Redemption's cost is its Reward's points_cost.
CREATE TABLE rewards (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	name        TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
	points_cost INTEGER NOT NULL CHECK (points_cost BETWEEN 1 AND 100000),
	archived    INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1))
);

CREATE TABLE redemptions (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	reward_id    INTEGER NOT NULL REFERENCES rewards (id),
	member_id    INTEGER NOT NULL REFERENCES members (id),
	requested_at INTEGER NOT NULL,
	status       TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'declined')),
	decided_at   INTEGER,
	decided_by   INTEGER REFERENCES members (id)
);

CREATE TABLE point_adjustments (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	member_id  INTEGER NOT NULL REFERENCES members (id),
	points     INTEGER NOT NULL CHECK (points <> 0),
	reason     TEXT NOT NULL CHECK (length(reason) BETWEEN 1 AND 200),
	created_at INTEGER NOT NULL,
	created_by INTEGER NOT NULL REFERENCES members (id)
);

-- A Member's balance: completions − approved Redemptions ± Adjustments.
CREATE VIEW balances AS
SELECT m.id AS member_id,
	coalesce((SELECT sum(points_awarded) FROM chore_completions WHERE member_id = m.id), 0)
	- coalesce((SELECT sum(rw.points_cost) FROM redemptions rd JOIN rewards rw ON rw.id = rd.reward_id
		WHERE rd.member_id = m.id AND rd.status = 'approved'), 0)
	+ coalesce((SELECT sum(points) FROM point_adjustments WHERE member_id = m.id), 0) AS points
FROM members m;
