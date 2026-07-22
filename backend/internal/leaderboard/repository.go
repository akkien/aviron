package leaderboard

import "context"

// Stats is one user's running leaderboard_alltime totals — the raw
// persisted counters LeaderboardService divides into rates/averages.
type Stats struct {
	TotalRaces       int
	TotalWins        int
	TotalPaceWattSum float64
}

// LeaderboardRepository is the persistence seam for the leaderboard domain.
// Defined here, next to its consumer (LeaderboardService), per this
// project's interface-placement convention.
type LeaderboardRepository interface {
	// GetUserStats returns userID's running totals. A user with no
	// leaderboard_alltime row yet (never finished a race) gets a zero-value
	// Stats and a nil error — that's a normal, expected state for a new
	// account, not an error condition.
	GetUserStats(ctx context.Context, userID string) (Stats, error)
}
