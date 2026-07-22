package leaderboard_test

import (
	"context"
	"errors"
	"testing"

	"github.com/akkien/aviron/internal/leaderboard"
)

func TestService_GetMyStats_ComputesAverage(t *testing.T) {
	repo := newFakeRepository()
	repo.stats["user-1"] = leaderboard.Stats{TotalRaces: 4, TotalWins: 2, TotalPaceWattSum: 200}
	svc := leaderboard.NewLeaderboardService(repo)

	got, err := svc.GetMyStats(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("GetMyStats() error = %v", err)
	}
	want := leaderboard.LeaderboardMeResponse{RacesJoined: 4, RacesWon: 2, AvgWPM: 50}
	if got != want {
		t.Errorf("GetMyStats() = %+v, want %+v", got, want)
	}
}

func TestService_GetMyStats_NoRacesYet_ZeroNotNaN(t *testing.T) {
	repo := newFakeRepository()
	svc := leaderboard.NewLeaderboardService(repo)

	got, err := svc.GetMyStats(context.Background(), "brand-new-user")
	if err != nil {
		t.Fatalf("GetMyStats() error = %v", err)
	}
	want := leaderboard.LeaderboardMeResponse{RacesJoined: 0, RacesWon: 0, AvgWPM: 0}
	if got != want {
		t.Errorf("GetMyStats() = %+v, want %+v", got, want)
	}
}

func TestService_GetMyStats_RepositoryError(t *testing.T) {
	wantErr := errors.New("boom")
	repo := &fakeRepository{err: wantErr}
	svc := leaderboard.NewLeaderboardService(repo)

	_, err := svc.GetMyStats(context.Background(), "user-1")
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}
