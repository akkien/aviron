package leaderboard_test

import (
	"context"

	"github.com/akkien/aviron/internal/leaderboard"
)

// fakeRepository is an in-memory leaderboard.LeaderboardRepository used by
// both service_test.go and handler_test.go, so neither needs a real
// Postgres connection. A missing map entry naturally returns a zero-value
// Stats, matching the real repository's "no row yet" contract.
type fakeRepository struct {
	stats map[string]leaderboard.Stats
	top   map[leaderboard.Window][]leaderboard.Entry
	err   error
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{
		stats: make(map[string]leaderboard.Stats),
		top:   make(map[leaderboard.Window][]leaderboard.Entry),
	}
}

func (f *fakeRepository) GetUserStats(ctx context.Context, userID string) (leaderboard.Stats, error) {
	if f.err != nil {
		return leaderboard.Stats{}, f.err
	}
	return f.stats[userID], nil
}

// GetTop slices [offset:offset+limit], mirroring the real repository's SQL
// LIMIT/OFFSET — lets a test seed more entries than one page and assert
// windowing/total both behave correctly. total is always the full seeded
// count for window, regardless of what page is sliced out.
func (f *fakeRepository) GetTop(ctx context.Context, window leaderboard.Window, limit, offset int) ([]leaderboard.Entry, int, error) {
	if f.err != nil {
		return nil, 0, f.err
	}
	all := f.top[window]
	total := len(all)

	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return all[offset:end], total, nil
}
