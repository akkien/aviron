-- Adds the running counters user-stats/user-stats.md needs to serve
-- GET /leaderboard/me without a live aggregate query: total_wins (races
-- finished at rank 1) and total_pace_watt_sum (sum of each race's
-- AvgPaceWatt, divided by total_races at read time for the average).

ALTER TABLE leaderboard_alltime ADD COLUMN total_wins INT NOT NULL DEFAULT 0;
ALTER TABLE leaderboard_alltime ADD COLUMN total_pace_watt_sum NUMERIC NOT NULL DEFAULT 0;
