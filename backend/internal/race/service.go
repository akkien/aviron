package race

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/akkien/aviron/internal/room"
)

type RaceService struct {
	repo      RaceRepository
	jwtSecret []byte
}

func NewRaceService(repo RaceRepository, jwtSecret []byte) *RaceService {
	return &RaceService{repo: repo, jwtSecret: jwtSecret}
}

// CreateRace validates and creates a new race. A non-empty fieldErrs return
// means validation failed and err is always nil in that case; err is only
// set for downstream repository failures. The creator is auto-added as a
// participant (and gets a session token back, same as JoinRace) — a race
// with zero participants until its creator separately calls Join was a
// pre-existing gap flagged back in the Start Race feature, now fixed here.
func (s *RaceService) CreateRace(ctx context.Context, name string, distanceMeters int, createdBy string) (r Race, sessionToken string, fieldErrs map[string]string, err error) {
	if errs := validateCreateRace(name, distanceMeters); len(errs) > 0 {
		return Race{}, "", errs, nil
	}

	r, err = s.repo.CreateRace(ctx, strings.TrimSpace(name), distanceMeters, createdBy)
	if err != nil {
		return Race{}, "", nil, err
	}

	if err = s.repo.AddParticipant(ctx, r.ID, createdBy); err != nil {
		return Race{}, "", nil, err
	}

	sessionToken, err = s.signSessionToken(r.ID, createdBy)
	if err != nil {
		return Race{}, "", nil, err
	}

	return r, sessionToken, nil, nil
}

// JoinRace adds userID as a participant of raceID and returns a signed
// per-race session token. Idempotent for a caller who's already a
// participant — pending, active, finished, or cancelled, it doesn't matter
// — they get a fresh token instead of an error, since the only way to
// recover a lost session_token (e.g. a page reload) is calling this again
// (idempotent-join.md). A token for a finished/cancelled race is harmless:
// the room actor behind it has already torn down and been removed from the
// registry, so a WS handshake attempt fails there regardless of token
// validity — no extra status guard needed for that case.
// ErrRaceNotFound, ErrRaceNotPending, and ErrRaceFull pass through as-is for
// the handler to map to responses; ErrAlreadyJoined is now only reachable
// via the TOCTOU window between the IsParticipant check below and
// AddParticipant's own insert (two concurrent first-time joins from the
// same user) — the same accepted count-then-insert race class this
// codebase already accepts for MaxParticipants.
func (s *RaceService) JoinRace(ctx context.Context, raceID, userID string) (sessionToken string, err error) {
	r, err := s.repo.GetRace(ctx, raceID)
	if err != nil {
		return "", err
	}

	alreadyIn, err := s.repo.IsParticipant(ctx, raceID, userID)
	if err != nil {
		return "", err
	}
	if alreadyIn {
		return s.signSessionToken(raceID, userID)
	}

	if r.Status != "pending" {
		return "", ErrRaceNotPending
	}

	count, err := s.repo.CountParticipants(ctx, raceID)
	if err != nil {
		return "", err
	}
	if count >= MaxParticipants {
		return "", ErrRaceFull
	}

	if err := s.repo.AddParticipant(ctx, raceID, userID); err != nil {
		return "", err
	}

	return s.signSessionToken(raceID, userID)
}

// signSessionToken signs the per-race WS handshake credential shared by
// CreateRace (creator auto-join) and JoinRace.
func (s *RaceService) signSessionToken(raceID, userID string) (string, error) {
	claims := jwt.MapClaims{
		"race_id": raceID,
		"user_id": userID,
		"exp":     time.Now().Add(6 * time.Hour).Unix(),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.jwtSecret)
	if err != nil {
		return "", fmt.Errorf("race: sign session token: %w", err)
	}
	return signed, nil
}

// LeaveRace removes userID as a participant of raceID. Only valid while the
// race is still pending — mirrors JoinRace's own pending-only rule. Once
// active, leaving must go through the WebSocket leave_race path
// (leave-race.md) instead, since the room actor is the only place holding
// authoritative participant state at that point, not race_participants.
func (s *RaceService) LeaveRace(ctx context.Context, raceID, userID string) error {
	r, err := s.repo.GetRace(ctx, raceID)
	if err != nil {
		return err
	}
	if r.Status != "pending" {
		return ErrRaceNotPending
	}
	return s.repo.RemoveParticipant(ctx, raceID, userID)
}

// StartRace generates the shared prompt text and flips raceID from pending
// to active. Only the race's creator may call this.
func (s *RaceService) StartRace(ctx context.Context, raceID, callerID string) (Race, error) {
	r, err := s.repo.GetRace(ctx, raceID)
	if err != nil {
		return Race{}, err
	}
	if r.CreatedBy != callerID {
		return Race{}, ErrNotCreator
	}
	if r.Status != "pending" {
		return Race{}, ErrRaceNotPending
	}

	promptText := generatePromptText(r.DistanceMeters)
	return s.repo.StartRace(ctx, raceID, promptText)
}

func (s *RaceService) GetRaceText(ctx context.Context, raceID string) (string, error) {
	return s.repo.GetRaceText(ctx, raceID)
}

func (s *RaceService) GetRaceDetail(ctx context.Context, raceID string) (RaceDetail, error) {
	return s.repo.GetRaceWithParticipants(ctx, raceID)
}

// ListOpenRaces returns pending, joinable races callerID hasn't already
// created or joined (open-races.md). Thin delegate — no validation or
// orchestration needed, same shape as GetRaceText/GetRaceDetail.
func (s *RaceService) ListOpenRaces(ctx context.Context, callerID string) ([]OpenRace, error) {
	return s.repo.ListOpenRaces(ctx, callerID)
}

// FinishRace persists a race's final results. Exists specifically to
// satisfy room.RaceFinisher (structurally — no import of internal/race from
// internal/room is needed, avoiding the cycle that a direct room -> race
// call would create) so the room actor can call it once a race completes
// (race-completion/finish-race.md). A thin delegate, not orchestration: the
// actual multi-statement transaction lives in the repository, per this
// project's Service-depends-on-Repository-interface convention.
func (s *RaceService) FinishRace(ctx context.Context, raceID string, distanceMeters int, results []room.ParticipantResult) error {
	return s.repo.FinishRace(ctx, raceID, distanceMeters, results)
}

// CancelRace persists a pending race's cancellation. Exists specifically to
// satisfy room.RaceCanceller (structurally, same import-cycle reasoning as
// FinishRace) so the room actor can call it when a pending room tears down
// before ever going active (room-lifecycle/cancelled-race-status.md). A thin
// delegate, same as FinishRace — the single UPDATE lives in the repository.
func (s *RaceService) CancelRace(ctx context.Context, raceID string) error {
	return s.repo.CancelRace(ctx, raceID)
}

func validateCreateRace(name string, distanceMeters int) map[string]string {
	errs := map[string]string{}

	trimmed := strings.TrimSpace(name)
	if len(trimmed) == 0 || len(name) > 100 {
		errs["name"] = "must be 1-100 characters"
	}
	if distanceMeters <= 0 {
		errs["distance_meters"] = "must be a positive integer"
	}

	return errs
}
