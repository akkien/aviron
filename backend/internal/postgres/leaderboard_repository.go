package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/akkien/aviron/internal/leaderboard"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type LeaderboardRepository struct {
	pool *pgxpool.Pool
}

func NewLeaderboardRepository(pool *pgxpool.Pool) *LeaderboardRepository {
	return &LeaderboardRepository{pool: pool}
}

// GetUserStats returns userID's leaderboard_alltime row. A user who's never
// finished a race has no row at all — that's a normal, expected state for a
// new account, so pgx.ErrNoRows maps to a zero-value Stats, not an error.
func (r *LeaderboardRepository) GetUserStats(ctx context.Context, userID string) (leaderboard.Stats, error) {
	var s leaderboard.Stats

	err := r.pool.QueryRow(ctx, `
		SELECT total_races, total_wins, total_pace_watt_sum
		FROM leaderboard_alltime
		WHERE user_id = $1
	`, userID).Scan(&s.TotalRaces, &s.TotalWins, &s.TotalPaceWattSum)

	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return leaderboard.Stats{}, nil
		}
		return leaderboard.Stats{}, fmt.Errorf("postgres: get user stats: %w", err)
	}

	return s, nil
}

// topAllTimeQuery reads straight off the already-maintained leaderboard_alltime
// aggregate (kept current by RaceRepository.FinishRace's own transaction) —
// total_races > 0 both guards the division and is implied anyway, since a
// row only ever exists once FinishRace has inserted one.
const topAllTimeQuery = `
	SELECT u.id, u.display_name, la.total_races, la.total_wins,
	       la.total_pace_watt_sum / la.total_races AS avg_wpm
	FROM leaderboard_alltime la
	JOIN users u ON u.id = la.user_id
	WHERE la.total_races > 0
	ORDER BY la.total_wins DESC, avg_wpm DESC
	LIMIT $1 OFFSET $2
`

// countAllTimeQuery mirrors topAllTimeQuery's WHERE clause with no
// ORDER BY/LIMIT/OFFSET — the total GetTop needs to compute a page count,
// since a windowed/offset query alone can't tell the caller how many pages
// exist beyond the one it fetched.
const countAllTimeQuery = `
	SELECT count(*) FROM leaderboard_alltime WHERE total_races > 0
`

// topWeeklyQuery has no maintained aggregate to read from, so it's computed
// live from race_participants/races each call — a rolling 7-day window
// (ranked-leaderboard.md's "no timezone/calendar-boundary handling needed"
// reasoning), not a calendar week. status = 'finished' excludes cancelled
// races a user may have joined, which never got a finish_rank.
const topWeeklyQuery = `
	SELECT u.id, u.display_name, count(*) AS races,
	       count(*) FILTER (WHERE rp.finish_rank = 1) AS wins,
	       avg(rp.avg_pace_watt) AS avg_wpm
	FROM race_participants rp
	JOIN races r ON r.id = rp.race_id
	JOIN users u ON u.id = rp.user_id
	WHERE r.status = 'finished' AND r.ended_at >= now() - interval '7 days'
	GROUP BY u.id, u.display_name
	ORDER BY wins DESC, avg_wpm DESC
	LIMIT $1 OFFSET $2
`

// countWeeklyQuery mirrors topWeeklyQuery's WHERE clause — count(DISTINCT
// user_id) matches that query's GROUP BY u.id, since each distinct user
// becomes exactly one row there.
const countWeeklyQuery = `
	SELECT count(DISTINCT rp.user_id)
	FROM race_participants rp
	JOIN races r ON r.id = rp.race_id
	WHERE r.status = 'finished' AND r.ended_at >= now() - interval '7 days'
`

// GetTop returns window's entries for one page (limit rows starting at
// offset), already ordered by wins (avg WPM as tiebreaker) —
// LeaderboardService only assigns rank by absolute position, it never
// re-sorts. total is a separate COUNT query rather than a window function
// (e.g. count(*) OVER()) — a page beyond the last one returns zero rows,
// and a window function's count would be unavailable exactly when the
// caller needs it most to realize the requested page doesn't exist.
func (r *LeaderboardRepository) GetTop(ctx context.Context, window leaderboard.Window, limit, offset int) ([]leaderboard.Entry, int, error) {
	query, countQuery := topAllTimeQuery, countAllTimeQuery
	if window == leaderboard.WindowWeekly {
		query, countQuery = topWeeklyQuery, countWeeklyQuery
	}

	var total int
	if err := r.pool.QueryRow(ctx, countQuery).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("postgres: get top leaderboard: count: %w", err)
	}

	rows, err := r.pool.Query(ctx, query, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("postgres: get top leaderboard: %w", err)
	}
	defer rows.Close()

	var entries []leaderboard.Entry
	for rows.Next() {
		var e leaderboard.Entry
		if err := rows.Scan(&e.UserID, &e.DisplayName, &e.Races, &e.Wins, &e.AvgWPM); err != nil {
			return nil, 0, fmt.Errorf("postgres: get top leaderboard: scan: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("postgres: get top leaderboard: rows: %w", err)
	}

	return entries, total, nil
}
