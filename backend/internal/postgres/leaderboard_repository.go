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
