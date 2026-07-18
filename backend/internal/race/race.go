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
}

var ErrRaceNotFound = errors.New("race: not found")
var ErrAlreadyJoined = errors.New("race: already joined")
var ErrRaceNotPending = errors.New("race: not pending")
