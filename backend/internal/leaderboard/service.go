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
