package consumer

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/akkien/aviron/internal/kafka"
	"github.com/akkien/aviron/internal/room"
)

// ErrPermanentWrite marks a WorkoutSampleWriter/FinishReconciler failure as
// permanent (e.g. a foreign-key violation from a nonexistent race_id) —
// retrying it would never succeed, so the consumer dead-letters the
// message and commits its offset instead of blocking on redelivery
// forever. Any other error is treated as transient (e.g. a dropped
// connection) and left uncommitted for kafka-go's own redelivery.
var ErrPermanentWrite = errors.New("consumer: permanent write failure")

// WorkoutSample is one workout.sample event, decoded off the wire.
// DistanceM is words typed correctly so far, PaceWatt is WPM
// (project-overview.md §13's telemetry-field-name holdovers).
type WorkoutSample struct {
	RaceID, UserID string
	Ts             time.Time
	DistanceM      float64
	PaceWatt       float64
}

// WorkoutSampleWriter persists a batch of samples (kafka-consumer-postgres-sink.md).
// Satisfied by *postgres.WorkoutSampleRepository — defined here, not in
// internal/postgres, so this package stays testable against a fake without
// importing pgx (same testability rationale coding-standards.md states for
// REST-domain Repository interfaces, applied to a non-REST-layered package).
type WorkoutSampleWriter interface {
	InsertBatch(ctx context.Context, samples []WorkoutSample) error
}

// FinishReconciler idempotently fills in any race_participants row
// FinishRace's own synchronous write missed. Satisfied by
// *postgres.RaceRepository's ReconcileParticipantResults.
type FinishReconciler interface {
	ReconcileParticipantResults(ctx context.Context, raceID string, results []room.RaceResultJSON) error
}

// DLQPublisher republishes an unprocessable message to its topic's
// dead-letter counterpart. Satisfied by *kafka.Producer's PublishRaw.
type DLQPublisher interface {
	PublishRaw(ctx context.Context, topic string, key, value []byte) error
}

const (
	workoutSampleDLQTopic = kafka.TopicWorkoutSample + ".dlq"
	raceFinishedDLQTopic  = kafka.TopicRaceFinished + ".dlq"

	// Two distinct group IDs, not one shared "aviron-consumer" — a real bug
	// caught during this feature's own live end-to-end verification:
	// kafka-go's Reader joins a real Kafka consumer group when GroupID is
	// set, and a group's rebalance/partition-assignment protocol assumes
	// every member subscribes to the same topic(s). Two readers sharing one
	// GroupID while each subscribes to a *different* topic (workout.sample
	// vs. race.finished) join as two members of one confused group, and the
	// resulting assignment silently starves at least one reader of any
	// partitions — confirmed live via kafka-consumer-groups.sh --describe
	// showing a stabilized 2-member group but zero messages ever reaching
	// either loop.
	workoutSampleGroupID = "aviron-consumer-workout-sample"
	raceFinishedGroupID  = "aviron-consumer-race-finished"

	flushBatchSize = 200
	flushInterval  = 3 * time.Second

	// fetchPollTimeout bounds each FetchMessage call so the workout.sample
	// loop wakes up periodically even on a quiet topic — otherwise a
	// blocking FetchMessage with no new messages would never let a
	// time-based flushInterval fire.
	fetchPollTimeout = 500 * time.Millisecond
)

// Consumer reads workout.sample/race.finished as a real Kafka consumer
// group and makes workout_samples real for the first time
// (kafka-consumer-postgres-sink.md's Overview).
type Consumer struct {
	brokers    []string
	writer     WorkoutSampleWriter
	reconciler FinishReconciler
	dlq        DLQPublisher
	logger     *slog.Logger
}

func NewConsumer(brokers []string, writer WorkoutSampleWriter, reconciler FinishReconciler, dlq DLQPublisher, logger *slog.Logger) *Consumer {
	return &Consumer{brokers: brokers, writer: writer, reconciler: reconciler, dlq: dlq, logger: logger}
}

// Run starts both reader loops (one per topic, each its own goroutine and
// consumer-group membership — no shared mutable state between them) and
// blocks until ctx is done, at which point each loop flushes its
// in-progress batch (if any) before exiting.
func (c *Consumer) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		c.runWorkoutSampleLoop(ctx, kafkago.NewReader(kafkago.ReaderConfig{
			Brokers: c.brokers,
			GroupID: workoutSampleGroupID,
			Topic:   kafka.TopicWorkoutSample,
		}))
	}()

	go func() {
		defer wg.Done()
		c.runRaceFinishedLoop(ctx, kafkago.NewReader(kafkago.ReaderConfig{
			Brokers: c.brokers,
			GroupID: raceFinishedGroupID,
			Topic:   kafka.TopicRaceFinished,
		}))
	}()

	wg.Wait()
	return nil
}

// deadLetter republishes msg verbatim to dlqTopic, logging (not returning)
// any publish failure — a DLQ is itself a best-effort safety net; there's
// no further fallback if publishing to it also fails, only a log line.
func (c *Consumer) deadLetter(ctx context.Context, dlqTopic string, msg kafkago.Message) {
	if err := c.dlq.PublishRaw(ctx, dlqTopic, msg.Key, msg.Value); err != nil {
		c.logger.Error("dead-letter publish failed", slog.String("topic", dlqTopic), slog.Any("error", err))
	}
}
