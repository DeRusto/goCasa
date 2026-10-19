-- Shopping lists (spec §6). A local list has no integration; a Mirrored list
-- (Mealie) sets integration_id and external_id, and its items external_id and
-- extra_json. Shopping is never locked.
CREATE TABLE shopping_lists (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	name           TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 50),
	integration_id INTEGER REFERENCES integrations (id) ON DELETE CASCADE,
	external_id    TEXT,
	hidden         INTEGER NOT NULL DEFAULT 0 CHECK (hidden IN (0, 1)),
	position       INTEGER NOT NULL
);

CREATE TABLE shopping_items (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	list_id     INTEGER NOT NULL REFERENCES shopping_lists (id) ON DELETE CASCADE,
	external_id TEXT,
	note        TEXT NOT NULL CHECK (length(note) BETWEEN 1 AND 200),
	checked     INTEGER NOT NULL DEFAULT 0 CHECK (checked IN (0, 1)),
	position    INTEGER NOT NULL,
	extra_json  TEXT
);
CREATE INDEX shopping_items_position ON shopping_items (list_id, position);
CREATE UNIQUE INDEX shopping_items_external ON shopping_items (list_id, external_id) WHERE external_id IS NOT NULL;
