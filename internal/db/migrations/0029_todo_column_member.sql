-- A to-do column may belong to one Member, whose Member page shows its to-dos.
-- An Archived Member keeps their columns; deleting a Member clears them.
ALTER TABLE todo_columns ADD COLUMN member_id INTEGER REFERENCES members (id) ON DELETE SET NULL;
