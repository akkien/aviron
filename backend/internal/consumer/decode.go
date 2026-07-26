package consumer

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/akkien/aviron/internal/room"
)

// workoutSampleMessage/raceFinishedMessage mirror internal/kafka's
// producer-side payload shapes by wire-format convention (matching JSON
// tags), not a shared Go type — producer and consumer are deliberately
// decoupled, only agreeing on each topic's JSON schema.
type workoutSampleMessage struct {
	RaceID    string    `json:"race_id"`
	UserID    string    `json:"user_id"`
	Ts        time.Time `json:"ts"`
	DistanceM float64   `json:"distance_m"`
	PaceWatt  float64   `json:"pace_watt"`
}

func decodeWorkoutSample(value []byte) (WorkoutSample, error) {
	var msg workoutSampleMessage
	if err := json.Unmarshal(value, &msg); err != nil {
		return WorkoutSample{}, fmt.Errorf("consumer: decode workout sample: %w", err)
	}
	return WorkoutSample{
		RaceID:    msg.RaceID,
		UserID:    msg.UserID,
		Ts:        msg.Ts,
		DistanceM: msg.DistanceM,
		PaceWatt:  msg.PaceWatt,
	}, nil
}

type raceFinishedMessage struct {
	RaceID  string                `json:"race_id"`
	Results []room.RaceResultJSON `json:"results"`
}

func decodeRaceFinished(value []byte) (raceFinishedMessage, error) {
	var msg raceFinishedMessage
	if err := json.Unmarshal(value, &msg); err != nil {
		return raceFinishedMessage{}, fmt.Errorf("consumer: decode race finished: %w", err)
	}
	return msg, nil
}
