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

func TestService_GetTop_InvalidWindow_ReturnsFieldError(t *testing.T) {
	repo := newFakeRepository()
	svc := leaderboard.NewLeaderboardService(repo)

	_, fieldErrs, err := svc.GetTop(context.Background(), "monthly", 10)
	if err != nil {
		t.Fatalf("GetTop() error = %v, want nil", err)
	}
	if fieldErrs["window"] == "" {
		t.Errorf("fieldErrs[\"window\"] empty, want a validation message")
	}
}

func TestService_GetTop_AssignsRankByPosition(t *testing.T) {
	repo := newFakeRepository()
	repo.top[leaderboard.WindowAllTime] = []leaderboard.Entry{
		{UserID: "user-1", DisplayName: "Alice", Races: 5, Wins: 3, AvgWPM: 60},
		{UserID: "user-2", DisplayName: "Bob", Races: 4, Wins: 2, AvgWPM: 55},
	}
	svc := leaderboard.NewLeaderboardService(repo)

	resp, fieldErrs, err := svc.GetTop(context.Background(), "alltime", 10)
	if err != nil {
		t.Fatalf("GetTop() error = %v", err)
	}
	if len(fieldErrs) > 0 {
		t.Fatalf("fieldErrs = %v, want none", fieldErrs)
	}
	if resp.Window != "alltime" {
		t.Errorf("Window = %q, want %q", resp.Window, "alltime")
	}
	if len(resp.Entries) != 2 {
		t.Fatalf("len(Entries) = %d, want 2", len(resp.Entries))
	}
	if resp.Entries[0].Rank != 1 || resp.Entries[0].UserID != "user-1" {
		t.Errorf("Entries[0] = %+v, want rank 1 for user-1", resp.Entries[0])
	}
	if resp.Entries[1].Rank != 2 || resp.Entries[1].UserID != "user-2" {
		t.Errorf("Entries[1] = %+v, want rank 2 for user-2", resp.Entries[1])
	}
}

func TestService_GetTop_NonPositiveLimit_UsesDefault(t *testing.T) {
	repo := newFakeRepository()
	entries := make([]leaderboard.Entry, 25)
	for i := range entries {
		entries[i] = leaderboard.Entry{UserID: "user"}
	}
	repo.top[leaderboard.WindowWeekly] = entries
	svc := leaderboard.NewLeaderboardService(repo)

	resp, _, err := svc.GetTop(context.Background(), "weekly", 0)
	if err != nil {
		t.Fatalf("GetTop() error = %v", err)
	}
	if len(resp.Entries) != 20 {
		t.Errorf("len(Entries) = %d, want default limit 20", len(resp.Entries))
	}
}

func TestService_GetTop_OversizedLimit_ClampedToMax(t *testing.T) {
	repo := newFakeRepository()
	entries := make([]leaderboard.Entry, 150)
	for i := range entries {
		entries[i] = leaderboard.Entry{UserID: "user"}
	}
	repo.top[leaderboard.WindowAllTime] = entries
	svc := leaderboard.NewLeaderboardService(repo)

	resp, _, err := svc.GetTop(context.Background(), "alltime", 1000)
	if err != nil {
		t.Fatalf("GetTop() error = %v", err)
	}
	if len(resp.Entries) != 100 {
		t.Errorf("len(Entries) = %d, want max limit 100", len(resp.Entries))
	}
}

func TestService_GetTop_RepositoryError(t *testing.T) {
	wantErr := errors.New("boom")
	repo := &fakeRepository{err: wantErr}
	svc := leaderboard.NewLeaderboardService(repo)

	_, _, err := svc.GetTop(context.Background(), "alltime", 10)
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}
