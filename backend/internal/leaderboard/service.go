package leaderboard

import (
	"context"
	"math"
)

type LeaderboardService struct {
	repo LeaderboardRepository
}

func NewLeaderboardService(repo LeaderboardRepository) *LeaderboardService {
	return &LeaderboardService{repo: repo}
}

// GetMyStats returns userID's dashboard stats. AvgWPM is total_pace_watt_sum
// divided by total_races at read time rather than a maintained column,
// guarding the zero-races case so a brand-new account gets 0, not NaN, and
// rounded to 2 decimal places so the client never has to (a raw division
// like 118/41 is a long repeating decimal otherwise).
func (s *LeaderboardService) GetMyStats(ctx context.Context, userID string) (LeaderboardMeResponse, error) {
	stats, err := s.repo.GetUserStats(ctx, userID)
	if err != nil {
		return LeaderboardMeResponse{}, err
	}

	var avgWPM float64
	if stats.TotalRaces > 0 {
		avgWPM = math.Round(stats.TotalPaceWattSum/float64(stats.TotalRaces)*100) / 100
	}

	return LeaderboardMeResponse{
		RacesJoined: stats.TotalRaces,
		RacesWon:    stats.TotalWins,
		AvgWPM:      avgWPM,
	}, nil
}

// defaultLimit/maxLimit bound GetTop's result size. A caller-requested
// limit is clamped, not rejected, so an oversized request degrades to the
// max rather than needing its own error path.
const (
	defaultLimit = 20
	maxLimit     = 100
)

// GetTop returns window's ranked entries (wins descending, AvgWPM as
// tiebreaker — repo.GetTop already returns them in that order, this only
// assigns rank by position). windowParam is raw, caller-supplied
// query-string input; anything other than "alltime"/"weekly" returns a
// field-keyed error the same way AuthService.Register's validation does,
// not a generic 500 — mirrors that method's (data, fieldErrs, err) shape.
func (s *LeaderboardService) GetTop(ctx context.Context, windowParam string, limit int) (resp LeaderboardTopResponse, fieldErrs map[string]string, err error) {
	window := Window(windowParam)
	if window != WindowAllTime && window != WindowWeekly {
		return LeaderboardTopResponse{}, map[string]string{"window": "must be alltime or weekly"}, nil
	}

	switch {
	case limit <= 0:
		limit = defaultLimit
	case limit > maxLimit:
		limit = maxLimit
	}

	entries, err := s.repo.GetTop(ctx, window, limit)
	if err != nil {
		return LeaderboardTopResponse{}, nil, err
	}

	resp = LeaderboardTopResponse{
		Window:  string(window),
		Entries: make([]LeaderboardEntryResponse, len(entries)),
	}
	for i, e := range entries {
		resp.Entries[i] = LeaderboardEntryResponse{
			Rank:        i + 1,
			UserID:      e.UserID,
			DisplayName: e.DisplayName,
			Races:       e.Races,
			Wins:        e.Wins,
			AvgWPM:      e.AvgWPM,
		}
	}

	return resp, nil, nil
}
