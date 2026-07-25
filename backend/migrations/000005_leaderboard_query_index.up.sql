-- Supports the ranked-leaderboard.md weekly-window query
-- (WHERE status = 'finished' AND ended_at >= ...), which had nothing to
-- use before this. Partial (only 'finished' rows) since every row that
-- query ever scans is already status = 'finished' — indexing
-- pending/active/cancelled rows too would only add write overhead for
-- zero read benefit.

CREATE INDEX idx_races_status_ended_at ON races (status, ended_at) WHERE status = 'finished';
