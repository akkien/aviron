package race_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/akkien/aviron/internal/race"
	"github.com/akkien/aviron/internal/room"
)

func TestRaceService_CreateRace_Success(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))

	r, _, fieldErrs, err := svc.CreateRace(context.Background(), "  Morning Sprint  ", 1000, "user-1")
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

			_, _, fieldErrs, err := svc.CreateRace(context.Background(), tt.raceName, tt.distanceMeters, "user-1")
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

	created, _, _, err := svc.CreateRace(ctx, "Morning Sprint", 1000, "user-1")
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

	created, _, _, err := svc.CreateRace(ctx, "Morning Sprint", 1000, "user-1")
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

func TestRaceService_JoinRace_RaceFull(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))
	ctx := context.Background()

	created, _, _, err := svc.CreateRace(ctx, "Morning Sprint", 1000, "user-1")
	if err != nil {
		t.Fatalf("CreateRace() error = %v", err)
	}
	// The creator already occupies one slot (auto-joined by CreateRace), so
	// only MaxParticipants-1 more joins fit before the race is full.
	for i := 0; i < race.MaxParticipants-1; i++ {
		userID := fmt.Sprintf("user-%d", i+2)
		if _, err := svc.JoinRace(ctx, created.ID, userID); err != nil {
			t.Fatalf("JoinRace(%s) error = %v", userID, err)
		}
	}

	_, err = svc.JoinRace(ctx, created.ID, "one-too-many")
	if !errors.Is(err, race.ErrRaceFull) {
		t.Errorf("err = %v, want ErrRaceFull", err)
	}
}

func TestRaceService_JoinRace_RaceNotPending(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))
	ctx := context.Background()

	created, _, _, err := svc.CreateRace(ctx, "Morning Sprint", 1000, "user-1")
	if err != nil {
		t.Fatalf("CreateRace() error = %v", err)
	}
	repo.races[0].Status = "active"

	_, err = svc.JoinRace(ctx, created.ID, "user-2")
	if !errors.Is(err, race.ErrRaceNotPending) {
		t.Errorf("err = %v, want ErrRaceNotPending", err)
	}
}

func TestRaceService_LeaveRace_Success(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))
	ctx := context.Background()

	created, _, _, err := svc.CreateRace(ctx, "Morning Sprint", 1000, "user-1")
	if err != nil {
		t.Fatalf("CreateRace() error = %v", err)
	}
	if _, err := svc.JoinRace(ctx, created.ID, "user-2"); err != nil {
		t.Fatalf("JoinRace() error = %v", err)
	}

	if err := svc.LeaveRace(ctx, created.ID, "user-2"); err != nil {
		t.Fatalf("LeaveRace() error = %v", err)
	}

	detail, err := svc.GetRaceDetail(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetRaceDetail() error = %v", err)
	}
	for _, p := range detail.Participants {
		if p.UserID == "user-2" {
			t.Error("user-2 still listed as a participant after LeaveRace")
		}
	}
}

func TestRaceService_LeaveRace_RaceNotFound(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))

	err := svc.LeaveRace(context.Background(), "nonexistent-race", "user-1")
	if !errors.Is(err, race.ErrRaceNotFound) {
		t.Errorf("err = %v, want ErrRaceNotFound", err)
	}
}

func TestRaceService_LeaveRace_NotParticipant(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))
	ctx := context.Background()

	created, _, _, err := svc.CreateRace(ctx, "Morning Sprint", 1000, "user-1")
	if err != nil {
		t.Fatalf("CreateRace() error = %v", err)
	}

	err = svc.LeaveRace(ctx, created.ID, "user-2")
	if !errors.Is(err, race.ErrNotParticipant) {
		t.Errorf("err = %v, want ErrNotParticipant", err)
	}
}

func TestRaceService_LeaveRace_RaceNotPending(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))
	ctx := context.Background()

	created, _, _, err := svc.CreateRace(ctx, "Morning Sprint", 1000, "user-1")
	if err != nil {
		t.Fatalf("CreateRace() error = %v", err)
	}
	if _, err := svc.JoinRace(ctx, created.ID, "user-2"); err != nil {
		t.Fatalf("JoinRace() error = %v", err)
	}
	repo.races[0].Status = "active"

	err = svc.LeaveRace(ctx, created.ID, "user-2")
	if !errors.Is(err, race.ErrRaceNotPending) {
		t.Errorf("err = %v, want ErrRaceNotPending", err)
	}
}

func TestRaceService_StartRace_Success(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))
	ctx := context.Background()

	created, _, _, err := svc.CreateRace(ctx, "Morning Sprint", 5, "user-1")
	if err != nil {
		t.Fatalf("CreateRace() error = %v", err)
	}

	started, err := svc.StartRace(ctx, created.ID, "user-1")
	if err != nil {
		t.Fatalf("StartRace() error = %v", err)
	}
	if started.Status != "active" {
		t.Errorf("Status = %q, want %q", started.Status, "active")
	}
	if started.StartedAt == nil {
		t.Error("StartedAt is nil, want set")
	}
	if started.PromptText == nil {
		t.Fatal("PromptText is nil, want set")
	}
	if got := len(strings.Fields(*started.PromptText)); got != 5 {
		t.Errorf("prompt word count = %d, want %d", got, 5)
	}
}

func TestRaceService_StartRace_NotCreator(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))
	ctx := context.Background()

	created, _, _, err := svc.CreateRace(ctx, "Morning Sprint", 5, "user-1")
	if err != nil {
		t.Fatalf("CreateRace() error = %v", err)
	}

	_, err = svc.StartRace(ctx, created.ID, "user-2")
	if !errors.Is(err, race.ErrNotCreator) {
		t.Errorf("err = %v, want ErrNotCreator", err)
	}
}

func TestRaceService_StartRace_NotFound(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))

	_, err := svc.StartRace(context.Background(), "nonexistent-race", "user-1")
	if !errors.Is(err, race.ErrRaceNotFound) {
		t.Errorf("err = %v, want ErrRaceNotFound", err)
	}
}

func TestRaceService_StartRace_NotPending(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))
	ctx := context.Background()

	created, _, _, err := svc.CreateRace(ctx, "Morning Sprint", 5, "user-1")
	if err != nil {
		t.Fatalf("CreateRace() error = %v", err)
	}
	if _, err := svc.StartRace(ctx, created.ID, "user-1"); err != nil {
		t.Fatalf("first StartRace() error = %v", err)
	}

	_, err = svc.StartRace(ctx, created.ID, "user-1")
	if !errors.Is(err, race.ErrRaceNotPending) {
		t.Errorf("err = %v, want ErrRaceNotPending", err)
	}
}

func TestRaceService_GetRaceText_Success(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))
	ctx := context.Background()

	created, _, _, err := svc.CreateRace(ctx, "Morning Sprint", 5, "user-1")
	if err != nil {
		t.Fatalf("CreateRace() error = %v", err)
	}
	if _, err := svc.StartRace(ctx, created.ID, "user-1"); err != nil {
		t.Fatalf("StartRace() error = %v", err)
	}

	text, err := svc.GetRaceText(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetRaceText() error = %v", err)
	}
	if text == "" {
		t.Error("text is empty, want the generated prompt")
	}
}

func TestRaceService_GetRaceText_NotReady(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))
	ctx := context.Background()

	created, _, _, err := svc.CreateRace(ctx, "Morning Sprint", 5, "user-1")
	if err != nil {
		t.Fatalf("CreateRace() error = %v", err)
	}

	_, err = svc.GetRaceText(ctx, created.ID)
	if !errors.Is(err, race.ErrPromptNotReady) {
		t.Errorf("err = %v, want ErrPromptNotReady", err)
	}
}

func TestRaceService_GetRaceText_NotFound(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))

	_, err := svc.GetRaceText(context.Background(), "nonexistent-race")
	if !errors.Is(err, race.ErrRaceNotFound) {
		t.Errorf("err = %v, want ErrRaceNotFound", err)
	}
}

func TestRaceService_GetRaceDetail_Success(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))
	ctx := context.Background()

	created, _, _, err := svc.CreateRace(ctx, "Morning Sprint", 5, "user-1")
	if err != nil {
		t.Fatalf("CreateRace() error = %v", err)
	}
	if _, err := svc.JoinRace(ctx, created.ID, "user-2"); err != nil {
		t.Fatalf("JoinRace() error = %v", err)
	}

	detail, err := svc.GetRaceDetail(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetRaceDetail() error = %v", err)
	}
	if detail.Name != "Morning Sprint" {
		t.Errorf("Name = %q, want %q", detail.Name, "Morning Sprint")
	}
	// user-1 (the creator, auto-joined by CreateRace) + user-2.
	if len(detail.Participants) != 2 {
		t.Fatalf("Participants = %d, want 2", len(detail.Participants))
	}
	if detail.Participants[1].UserID != "user-2" {
		t.Errorf("Participants[1].UserID = %q, want %q", detail.Participants[1].UserID, "user-2")
	}
}

func TestRaceService_GetRaceDetail_NotFound(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))

	_, err := svc.GetRaceDetail(context.Background(), "nonexistent-race")
	if !errors.Is(err, race.ErrRaceNotFound) {
		t.Errorf("err = %v, want ErrRaceNotFound", err)
	}
}

func TestRaceService_FinishRace_DelegatesToRepository(t *testing.T) {
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, []byte("test-secret"))

	rank := 1
	finishTimeMs := int64(45000)
	results := []room.ParticipantResult{
		{UserID: "user-1", FinishRank: &rank, FinishTimeMs: &finishTimeMs, DisconnectedCount: 0},
	}

	if err := svc.FinishRace(context.Background(), "race-1", 1000, results); err != nil {
		t.Fatalf("FinishRace() error = %v", err)
	}

	if len(repo.finishCalls) != 1 {
		t.Fatalf("finishCalls = %d, want 1", len(repo.finishCalls))
	}
	call := repo.finishCalls[0]
	if call.raceID != "race-1" {
		t.Errorf("raceID = %q, want %q", call.raceID, "race-1")
	}
	if call.distanceMeters != 1000 {
		t.Errorf("distanceMeters = %d, want 1000", call.distanceMeters)
	}
	if len(call.results) != 1 || call.results[0].UserID != "user-1" {
		t.Errorf("results = %+v, want the one user-1 result passed in", call.results)
	}
}

func TestRaceService_CancelRace_DelegatesToRepository(t *testing.T) {
	secret := []byte("test-secret")
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, secret)
	created, _, _, err := svc.CreateRace(context.Background(), "Morning Sprint", 1000, "creator")
	if err != nil {
		t.Fatalf("CreateRace() error = %v", err)
	}

	if err := svc.CancelRace(context.Background(), created.ID); err != nil {
		t.Fatalf("CancelRace() error = %v", err)
	}

	if len(repo.cancelCalls) != 1 || repo.cancelCalls[0] != created.ID {
		t.Errorf("cancelCalls = %v, want [%q]", repo.cancelCalls, created.ID)
	}
	if repo.races[0].Status != "cancelled" {
		t.Errorf("race status = %q, want %q", repo.races[0].Status, "cancelled")
	}
}

// TestRaceService_CancelRace_NoOpIfNotPending confirms the repository's
// status = 'pending' guard: cancelling a race that already moved on (e.g.
// a real race that finished around the same time a stray cancel arrived)
// must not clobber its real status, and must not error either — it's a
// no-op, not a failure.
func TestRaceService_CancelRace_NoOpIfNotPending(t *testing.T) {
	secret := []byte("test-secret")
	repo := newFakeRepository()
	svc := race.NewRaceService(repo, secret)
	created, _, _, err := svc.CreateRace(context.Background(), "Morning Sprint", 1000, "creator")
	if err != nil {
		t.Fatalf("CreateRace() error = %v", err)
	}
	repo.races[0].Status = "active"

	if err := svc.CancelRace(context.Background(), created.ID); err != nil {
		t.Fatalf("CancelRace() error = %v", err)
	}

	if repo.races[0].Status != "active" {
		t.Errorf("race status = %q, want unchanged %q", repo.races[0].Status, "active")
	}
}
