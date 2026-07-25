package leaderboard

import "context"

// Stats is one user's running leaderboard_alltime totals — the raw
// persisted counters LeaderboardService divides into rates/averages.
type Stats struct {
	TotalRaces       int
	TotalWins        int
	TotalPaceWattSum float64
}

// Window selects which slice of history GetTop ranks over.
type Window string

const (
	WindowAllTime Window = "alltime"
	WindowWeekly  Window = "weekly"
)

// Entry is one ranked row: a user's totals over the requested Window,
// already aggregated (AvgWPM computed) by the repository, since GetTop
// needs it for ORDER BY regardless of what the caller does with it after.
type Entry struct {
	UserID      string
	DisplayName string
	Races       int
	Wins        int
	AvgWPM      float64
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

	// GetTop returns one page of entries for window (limit rows starting
	// at offset), ranked by wins (AvgWPM as tiebreaker) — already sorted,
	// LeaderboardService only needs to assign rank by absolute position
	// (offset-aware, not just position within the page). total is the
	// count of all entries matching window, regardless of limit/offset,
	// so the caller can compute a total page count.
	GetTop(ctx context.Context, window Window, limit, offset int) (entries []Entry, total int, err error)
}
