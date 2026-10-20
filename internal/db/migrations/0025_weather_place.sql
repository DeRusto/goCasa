-- The place the weather location was picked as, e.g. "London, England, United
-- Kingdom"; '' when it was entered as coordinates or there is no weather.
ALTER TABLE household_settings ADD COLUMN weather_place TEXT NOT NULL DEFAULT '';
