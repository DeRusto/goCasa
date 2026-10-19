-- To-dos in columns (spec §6). Recurring to-dos add their columns later.
CREATE TABLE todo_columns (
	id       INTEGER PRIMARY KEY AUTOINCREMENT,
	name     TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 50),
	position INTEGER NOT NULL
);

CREATE TABLE todos (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	column_id    INTEGER NOT NULL REFERENCES todo_columns (id) ON DELETE CASCADE,
	title        TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
	notes        TEXT NOT NULL DEFAULT '' CHECK (length(notes) <= 5000),
	due_date     TEXT, -- YYYY-MM-DD
	position     INTEGER NOT NULL,
	completed_at INTEGER,
	created_at   INTEGER NOT NULL
);
CREATE INDEX todos_column_position ON todos (column_id, position);
