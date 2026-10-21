-- Fictional household for README screenshots. Times are relative to today, local zone.
PRAGMA foreign_keys = ON;

INSERT INTO members (id, name, color, role, created_at, pet) VALUES
 (1, 'Sam',     '#3b82f6', 'member', unixepoch(), 0),
 (2, 'Jordan',  '#ec4899', 'member', unixepoch(), 0),
 (3, 'Maya',    '#22c55e', 'member', unixepoch(), 0),
 (4, 'Leo',     '#f59e0b', 'member', unixepoch(), 0),
 (5, 'Biscuit', '#a855f7', 'member', unixepoch(), 1);

UPDATE household_settings SET units = 'imperial', clock_24h = 0, weather_lat = 47.6062, weather_lon = -122.3321,
  weather_place = 'Seattle, Washington', timezone = 'America/Los_Angeles', night_mode = 'dim_clock';

INSERT INTO source_calendars (id, integration_id, name, color) VALUES (1, 1, 'Family', '#3b82f6');

CREATE TEMP TABLE ev (id INTEGER, day INTEGER, s TEXT, e TEXT, title TEXT, loc TEXT, who TEXT);
INSERT INTO ev VALUES
 (1,  0, '07:30', '08:15', 'School drop-off', '', '1'),
 (2,  0, '09:00', '10:00', 'Standup + planning', '', '2'),
 (3,  0, '15:30', '17:00', 'Soccer practice', 'Green Lake Park', '3'),
 (4,  0, '18:30', '19:30', 'Family dinner at Nana''s', '', '1,2,3,4'),
 (5,  1, '08:00', '08:30', 'Vet: Biscuit checkup', 'Ballard Animal Clinic', '2,5'),
 (6,  1, '16:00', '17:00', 'Piano lesson', '', '4'),
 (7,  1, '19:00', '20:30', 'Book club', '', '2'),
 (8,  2, '10:00', '11:00', 'Dentist', '', '1'),
 (9,  2, '15:30', '17:00', 'Soccer practice', 'Green Lake Park', '3'),
 (10, 3, '12:00', '13:00', 'Lunch with Priya', '', '2'),
 (11, 3, '17:30', '18:30', 'Swim class', 'Community pool', '4'),
 (12, 4, '09:00', '12:00', 'Farmers market', 'Ballard', '1,2,3,4'),
 (13, 4, '14:00', '16:00', 'Maya''s birthday party', '', '1,2,3,4'),
 (14, 5, '10:00', '11:30', 'Hike: Discovery Park', '', '1,3,5'),
 (15, -1, '18:00', '19:00', 'Parent-teacher night', 'Lincoln Elementary', '1,2'),
 (16, -2, '09:30', '10:30', 'Car service', '', '1'),
 (17, 6, '16:00', '17:00', 'Piano lesson', '', '4');

INSERT INTO events (id, calendar_id, title, location, all_day, start_utc, end_utc, start_tzid, updated_at)
SELECT id, 1, title, loc, 0,
  unixepoch(date('now', 'localtime', day || ' days') || ' ' || s, 'utc'),
  unixepoch(date('now', 'localtime', day || ' days') || ' ' || e, 'utc'),
  'America/Los_Angeles', unixepoch()
FROM ev;

INSERT INTO events (id, calendar_id, title, all_day, all_day_date, all_day_end_date, updated_at) VALUES
 (30, 1, 'Grandma visiting', 1, date('now', 'localtime', '2 days'), date('now', 'localtime', '5 days'), unixepoch()),
 (31, 1, 'No school: teacher training', 1, date('now', 'localtime', '3 days'), date('now', 'localtime', '4 days'), unixepoch());

INSERT INTO event_members (event_id, member_id)
SELECT ev.id, j.value FROM ev, json_each('[' || ev.who || ']') j;
INSERT INTO event_members VALUES (30, 1), (30, 2), (30, 3), (30, 4), (31, 3), (31, 4);

INSERT INTO todo_columns (id, name, position) VALUES (1, 'Sam', 0), (2, 'Jordan', 1), (3, 'House', 2);
INSERT INTO todos (column_id, title, due_date, position, created_at, completed_at) VALUES
 (1, 'Renew car registration', date('now', 'localtime', '3 days'), 0, unixepoch(), NULL),
 (1, 'Book flights for the holidays', NULL, 1, unixepoch(), NULL),
 (1, 'Return library books', date('now', 'localtime'), 2, unixepoch(), unixepoch()),
 (2, 'Order Maya''s birthday cake', date('now', 'localtime', '2 days'), 0, unixepoch(), NULL),
 (2, 'Sign field trip form', date('now', 'localtime', '1 days'), 1, unixepoch(), NULL),
 (2, 'Call the plumber', NULL, 2, unixepoch(), NULL),
 (3, 'Replace furnace filter', date('now', 'localtime', '-1 days'), 0, unixepoch(), NULL),
 (3, 'Clean out the gutters', NULL, 1, unixepoch(), NULL),
 (3, 'Put up the porch lights', NULL, 2, unixepoch(), NULL);

INSERT INTO shopping_lists (id, name, position) VALUES (1, 'Groceries', 0), (2, 'Hardware store', 1);
INSERT INTO shopping_items (list_id, note, checked, position) VALUES
 (1, 'Milk', 0, 0), (1, 'Eggs (dozen)', 0, 1), (1, 'Sourdough bread', 0, 2), (1, 'Apples', 0, 3),
 (1, 'Pasta', 0, 4), (1, 'Parmesan', 0, 5), (1, 'Birthday candles', 0, 6), (1, 'Coffee beans', 1, 7), (1, 'Bananas', 1, 8),
 (2, 'Furnace filter 16x25', 0, 0), (2, 'AA batteries', 0, 1), (2, 'Wood screws', 1, 2);

INSERT INTO chores (id, name, schedule, days, kind, points, created_at) VALUES
 (1, 'Make your bed', 'daily', '', 'each', 5, unixepoch()),
 (2, 'Feed Biscuit', 'daily', '', 'anyone', 5, unixepoch()),
 (3, 'Empty the dishwasher', 'daily', '', 'anyone', 10, unixepoch()),
 (4, 'Take out the recycling', 'weekdays', 'TU,FR', 'anyone', 10, unixepoch()),
 (5, 'Tidy your room', 'weekly', '', 'each', 20, unixepoch()),
 (6, 'Practice piano', 'daily', '', 'each', 10, unixepoch());
INSERT INTO chore_assignees VALUES (1, 3), (1, 4), (2, 3), (2, 4), (3, 3), (3, 4), (4, 1), (4, 2), (5, 3), (5, 4), (6, 4);
INSERT INTO chore_completions (chore_id, member_id, round_start, completed_at, points_awarded) VALUES
 (1, 3, date('now', 'localtime'), unixepoch(), 5),
 (2, 4, date('now', 'localtime'), unixepoch(), 5),
 (1, 3, date('now', 'localtime', '-1 days'), unixepoch() - 86400, 5),
 (1, 4, date('now', 'localtime', '-1 days'), unixepoch() - 86400, 5),
 (3, 3, date('now', 'localtime', '-1 days'), unixepoch() - 86400, 10);

INSERT INTO rewards (name, points_cost) VALUES ('Pick the movie', 50), ('Extra 30 min screen time', 75), ('Ice cream trip', 150);
