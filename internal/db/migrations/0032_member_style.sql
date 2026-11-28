-- A Member's page style: every device shows that Member's page the same way.
ALTER TABLE members ADD COLUMN style TEXT NOT NULL DEFAULT 'standard' CHECK (style IN ('standard', 'kid'));
