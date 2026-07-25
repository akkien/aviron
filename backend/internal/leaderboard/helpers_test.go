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

// GetTop truncates to limit, mirroring the real repository's SQL LIMIT —
// lets a test seed more entries than a given limit and assert truncation.
func (f *fakeRepository) GetTop(ctx context.Context, window leaderboard.Window, limit int) ([]leaderboard.Entry, error) {
	if f.err != nil {
		return nil, f.err
	}
	entries := f.top[window]
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}
