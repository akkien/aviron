package race_test

import (
	"context"
	"testing"

	"github.com/akkien/aviron/internal/race"
)

func TestRaceService_CreateRace_Success(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo)

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
			svc := race.NewRaceService(repo)

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
