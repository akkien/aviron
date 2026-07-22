package race

import (
	"errors"
	"time"
)

type Race struct {
	ID             string
	Name           string
	DistanceMeters int
	Status         string
	CreatedBy      string
	CreatedAt      time.Time
	StartedAt      *time.Time
	PromptText     *string
}

type Participant struct {
	UserID      string
	DisplayName string
	JoinedAt    time.Time
	// FinishRank/FinishTimeMs/AvgPaceWatt are nil/zero until the race
	// finishes — same nullable shape as room.RaceResultJSON
	// (race-detail-cold-visit.md), populated by FinishRace's transaction.
	FinishRank   *int
	FinishTimeMs *int64
	AvgPaceWatt  float64
}

type RaceDetail struct {
	Race
	Participants []Participant
}

// MaxParticipants caps how many players can join a single race — the room
// actor (Phase 2) holds every participant's state in memory and broadcasts
// a snapshot including all of them on every tick, so this also bounds the
// per-tick broadcast payload size.
const MaxParticipants = 10

var ErrRaceNotFound = errors.New("race: not found")
var ErrAlreadyJoined = errors.New("race: already joined")
var ErrRaceNotPending = errors.New("race: not pending")
var ErrNotCreator = errors.New("race: caller is not the creator")
var ErrPromptNotReady = errors.New("race: prompt text not ready")
var ErrRaceFull = errors.New("race: full")

// ErrNotParticipant is returned by LeaveRace (leave-race.md) when the caller
// was never a participant of the race — never joined, or already left.
var ErrNotParticipant = errors.New("race: caller is not a participant")
