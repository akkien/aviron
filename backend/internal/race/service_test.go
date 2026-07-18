package race_test

import (
	"context"
	"errors"
	"testing"

	"github.com/akkien/aviron/internal/race"
)

func TestRaceService_CreateRace_Success(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))

	r, fieldErrs, err := svc.CreateRace(context.Background(), "  Morning Sprint  ", 1000, "user-1")
	if err != nil {
		t.Fatalf("CreateRace() error = %v", err)
	}
	if len(fieldErrs) != 0 {
		t.Fatalf("fieldErrs = %v, want none", fieldErrs)
	}
	if r.Name != "Morning Sprint" {
		t.Errorf("Name = %q, want trimmed %q", r.Name, "Morning Sprint")
	}
	if r.Status != "pending" {
		t.Errorf("Status = %q, want %q", r.Status, "pending")
	}
	if r.CreatedBy != "user-1" {
		t.Errorf("CreatedBy = %q, want %q", r.CreatedBy, "user-1")
	}
	if r.DistanceMeters != 1000 {
		t.Errorf("DistanceMeters = %d, want %d", r.DistanceMeters, 1000)
	}
}

func TestRaceService_CreateRace_ValidationErrors(t *testing.T) {
	tests := []struct {
		name           string
		raceName       string
		distanceMeters int
		wantField      string
	}{
		{"empty name", "", 1000, "name"},
		{"whitespace-only name", "   ", 1000, "name"},
		{"zero distance", "Morning Sprint", 0, "distance_meters"},
		{"negative distance", "Morning Sprint", -5, "distance_meters"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := newFakeRepository()
			svc := race.NewRaceService(repo, []byte("test-secret"))

			_, fieldErrs, err := svc.CreateRace(context.Background(), tt.raceName, tt.distanceMeters, "user-1")
			if err != nil {
				t.Fatalf("CreateRace() error = %v, want nil", err)
			}
			if _, ok := fieldErrs[tt.wantField]; !ok {
				t.Errorf("fieldErrs = %v, want key %q", fieldErrs, tt.wantField)
			}
		})
	}
}

func TestRaceService_JoinRace_Success(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))
	ctx := context.Background()

	created, _, err := svc.CreateRace(ctx, "Morning Sprint", 1000, "user-1")
	if err != nil {
		t.Fatalf("CreateRace() error = %v", err)
	}

	token, err := svc.JoinRace(ctx, created.ID, "user-2")
	if err != nil {
		t.Fatalf("JoinRace() error = %v", err)
	}
	if token == "" {
		t.Error("token is empty, want a signed JWT")
	}
}

func TestRaceService_JoinRace_RaceNotFound(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))

	_, err := svc.JoinRace(context.Background(), "nonexistent-race", "user-1")
	if !errors.Is(err, race.ErrRaceNotFound) {
		t.Errorf("err = %v, want ErrRaceNotFound", err)
	}
}

func TestRaceService_JoinRace_AlreadyJoined(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))
	ctx := context.Background()

	created, _, err := svc.CreateRace(ctx, "Morning Sprint", 1000, "user-1")
	if err != nil {
		t.Fatalf("CreateRace() error = %v", err)
	}
	if _, err := svc.JoinRace(ctx, created.ID, "user-2"); err != nil {
		t.Fatalf("first JoinRace() error = %v", err)
	}

	_, err = svc.JoinRace(ctx, created.ID, "user-2")
	if !errors.Is(err, race.ErrAlreadyJoined) {
		t.Errorf("err = %v, want ErrAlreadyJoined", err)
	}
}

func TestRaceService_JoinRace_RaceNotPending(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))
	ctx := context.Background()

	created, _, err := svc.CreateRace(ctx, "Morning Sprint", 1000, "user-1")
	if err != nil {
		t.Fatalf("CreateRace() error = %v", err)
	}
	repo.races[0].Status = "active"

	_, err = svc.JoinRace(ctx, created.ID, "user-2")
	if !errors.Is(err, race.ErrRaceNotPending) {
		t.Errorf("err = %v, want ErrRaceNotPending", err)
	}
}
