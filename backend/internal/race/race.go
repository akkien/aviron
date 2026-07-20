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
