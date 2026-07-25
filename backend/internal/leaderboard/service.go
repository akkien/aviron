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

// pageSize is fixed, not caller-configurable — the client only ever asks
// for a page number, matching the frontend's own fixed 5-per-page display
// rather than exposing a generic limit/offset pair over the wire.
const pageSize = 5

// GetTop returns window's ranked entries for one page (wins descending,
// AvgWPM as tiebreaker — repo.GetTop already returns them in that order,
// this only assigns rank by absolute position, offset-aware so page 2's
// first entry is rank 6, not rank 1). windowParam is raw, caller-supplied
// query-string input; anything other than "alltime"/"weekly" returns a
// field-keyed error the same way AuthService.Register's validation does,
// not a generic 500 — mirrors that method's (data, fieldErrs, err) shape.
// page is 1-indexed; anything less than 1 is treated as 1 rather than
// rejected, the same "clamp, don't reject" precedent this method already
// used for limit before pagination replaced it.
func (s *LeaderboardService) GetTop(ctx context.Context, windowParam string, page int) (resp LeaderboardTopResponse, fieldErrs map[string]string, err error) {
	window := Window(windowParam)
	if window != WindowAllTime && window != WindowWeekly {
		return LeaderboardTopResponse{}, map[string]string{"window": "must be alltime or weekly"}, nil
	}

	if page < 1 {
		page = 1
	}
	offset := (page - 1) * pageSize

	entries, total, err := s.repo.GetTop(ctx, window, pageSize, offset)
	if err != nil {
		return LeaderboardTopResponse{}, nil, err
	}

	resp = LeaderboardTopResponse{
		Window:     string(window),
		Page:       page,
		TotalPages: max(1, (total+pageSize-1)/pageSize),
		Entries:    make([]LeaderboardEntryResponse, len(entries)),
	}
	for i, e := range entries {
		resp.Entries[i] = LeaderboardEntryResponse{
			Rank:        offset + i + 1,
			UserID:      e.UserID,
			DisplayName: e.DisplayName,
			Races:       e.Races,
			Wins:        e.Wins,
			AvgWPM:      e.AvgWPM,
		}
	}

	return resp, nil, nil
}
