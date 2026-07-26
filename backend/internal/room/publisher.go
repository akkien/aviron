package room

import (
	"context"
	"time"
)

// EventPublisher publishes room activity to Kafka (kafka-producer.md) —
// workout.sample per telemetry event, race.finished once a race actually
// completes. A Registry-level dependency (like TickObserver/RoomLocator),
// not a per-Spawn argument: every room shares the same process-wide
// publisher. Defined here, not in internal/kafka, for the same import-
// cycle reason as RaceFinisher/RaceLeaver/RaceCanceller (finish.go): the
// concrete implementation only needs this package's own ParticipantResult
// type, not the reverse.
type EventPublisher interface {
	PublishWorkoutSample(ctx context.Context, raceID, userID string, ts time.Time, wordsCorrect int, paceWatt float64) error
	PublishRaceFinished(ctx context.Context, raceID string, results []ParticipantResult) error
}

// NoopPublisher is the EventPublisher for single-instance/no-Kafka local
// dev and every existing test fixture that constructs a Registry — mirrors
// NoopLocator (registry.go) exactly.
type NoopPublisher struct{}

func (NoopPublisher) PublishWorkoutSample(ctx context.Context, raceID, userID string, ts time.Time, wordsCorrect int, paceWatt float64) error {
	return nil
}

func (NoopPublisher) PublishRaceFinished(ctx context.Context, raceID string, results []ParticipantResult) error {
	return nil
}
