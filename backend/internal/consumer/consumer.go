package consumer

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
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

// MetricsRecorder receives this package's own push-model metrics (batch
// insert duration/errors, DLQ publishes) without internal/consumer needing
// to import prometheus directly — the same one-directional shape
// internal/room's TickObserver already establishes, so internal/metrics can
// depend on internal/consumer without internal/consumer ever needing to
// import internal/metrics (metrics/metrics-parity.md). Satisfied by
// *metrics.ConsumerMetrics.
type MetricsRecorder interface {
	// ObserveBatchInsert records one WorkoutSampleWriter.InsertBatch or
	// FinishReconciler.ReconcileParticipantResults call's duration; err is
	// nil on success.
	ObserveBatchInsert(topic string, d time.Duration, err error)
	// IncDLQ records one DLQPublisher.PublishRaw call, regardless of
	// whether the publish itself succeeds.
	IncDLQ(topic string)
}

// NoopMetricsRecorder discards every observation — the default for tests
// that don't care about metrics (mirrors internal/room's Noop* collaborators).
type NoopMetricsRecorder struct{}

func (NoopMetricsRecorder) ObserveBatchInsert(topic string, d time.Duration, err error) {}
func (NoopMetricsRecorder) IncDLQ(topic string)                                         {}

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
	metrics    MetricsRecorder
	logger     *slog.Logger

	// workoutReader/raceFinishedReader are stored (not kept local to Run's
	// own goroutines) so Lag can read their Stats() from outside — set once
	// via atomic.Pointer.Store before each loop's goroutine starts, read via
	// Load from a metrics scrape goroutine that may run concurrently with
	// Run itself (metrics/metrics-parity.md). *kafkago.Reader.Stats() is
	// documented safe for concurrent use alongside the reader's own fetch
	// loop, but the pointer's own publication still needs synchronizing.
	workoutReader      atomic.Pointer[kafkago.Reader]
	raceFinishedReader atomic.Pointer[kafkago.Reader]
}

func NewConsumer(brokers []string, writer WorkoutSampleWriter, reconciler FinishReconciler, dlq DLQPublisher, metrics MetricsRecorder, logger *slog.Logger) *Consumer {
	return &Consumer{brokers: brokers, writer: writer, reconciler: reconciler, dlq: dlq, metrics: metrics, logger: logger}
}

// Run starts both reader loops (one per topic, each its own goroutine and
// consumer-group membership — no shared mutable state between them) and
// blocks until ctx is done, at which point each loop flushes its
// in-progress batch (if any) before exiting.
func (c *Consumer) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(2)

	workoutReader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers: c.brokers,
		GroupID: workoutSampleGroupID,
		Topic:   kafka.TopicWorkoutSample,
	})
	c.workoutReader.Store(workoutReader)
	go func() {
		defer wg.Done()
		c.runWorkoutSampleLoop(ctx, workoutReader)
	}()

	raceFinishedReader := kafkago.NewReader(kafkago.ReaderConfig{
		Brokers: c.brokers,
		GroupID: raceFinishedGroupID,
		Topic:   kafka.TopicRaceFinished,
	})
	c.raceFinishedReader.Store(raceFinishedReader)
	go func() {
		defer wg.Done()
		c.runRaceFinishedLoop(ctx, raceFinishedReader)
	}()

	wg.Wait()
	return nil
}

// Lag returns each topic's reader's current consumer-group lag, in
// messages (aviron_kafka_consumer_lag). Zero before Run's corresponding
// reader has been constructed yet.
func (c *Consumer) Lag() (workoutSample, raceFinished int64) {
	if r := c.workoutReader.Load(); r != nil {
		workoutSample = r.Stats().Lag
	}
	if r := c.raceFinishedReader.Load(); r != nil {
		raceFinished = r.Stats().Lag
	}
	return workoutSample, raceFinished
}

// deadLetter republishes msg verbatim to dlqTopic, logging (not returning)
// any publish failure — a DLQ is itself a best-effort safety net; there's
// no further fallback if publishing to it also fails, only a log line.
func (c *Consumer) deadLetter(ctx context.Context, dlqTopic string, msg kafkago.Message) {
	c.metrics.IncDLQ(dlqTopic)
	if err := c.dlq.PublishRaw(ctx, dlqTopic, msg.Key, msg.Value); err != nil {
		c.logger.Error("dead-letter publish failed", slog.String("topic", dlqTopic), slog.Any("error", err))
	}
}
