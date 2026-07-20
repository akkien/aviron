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

var ErrRaceNotFound = errors.New("race: not found")
var ErrAlreadyJoined = errors.New("race: already joined")
var ErrRaceNotPending = errors.New("race: not pending")
var ErrNotCreator = errors.New("race: caller is not the creator")
var ErrPromptNotReady = errors.New("race: prompt text not ready")
