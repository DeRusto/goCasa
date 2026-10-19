-- Timer settings are at most a day: browsers fire a setTimeout over about
-- 24.8 days (2^31 ms) at once. SQLite can't add a CHECK to an existing column.
UPDATE household_settings SET screensaver_idle_seconds = min(screensaver_idle_seconds, 86400), seconds_per_photo = min(seconds_per_photo, 86400);
CREATE TRIGGER household_settings_timer_cap BEFORE UPDATE OF screensaver_idle_seconds, seconds_per_photo ON household_settings
WHEN NEW.screensaver_idle_seconds > 86400 OR NEW.seconds_per_photo > 86400
BEGIN
	SELECT RAISE(ABORT, 'timer settings are at most 86400 seconds');
END;

-- The balances view sums each Member's history.
CREATE INDEX chore_completions_member ON chore_completions (member_id);
CREATE INDEX redemptions_member ON redemptions (member_id, status);
CREATE INDEX point_adjustments_member ON point_adjustments (member_id);
