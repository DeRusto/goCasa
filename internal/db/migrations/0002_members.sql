-- Members (spec §6). Avatars, archiving and PIN blocking come in a later migration.
CREATE TABLE members (
	id         INTEGER PRIMARY KEY AUTOINCREMENT, -- never reused: unlock cookies name an Admin by id
	name       TEXT NOT NULL CHECK (length(name) BETWEEN 1 AND 50),
	color      TEXT NOT NULL CHECK (color GLOB '#[0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f][0-9a-f]'),
	role       TEXT NOT NULL CHECK (role IN ('admin', 'member')),
	-- Only Admins have a PIN, and every Admin has one.
	pin_hash   BLOB CHECK ((role = 'admin') = (pin_hash IS NOT NULL)),
	created_at INTEGER NOT NULL
);
