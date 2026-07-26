package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/akkien/aviron/internal/room"
)

// Topic names exactly as project-overview.md §6 specifies.
const (
	TopicWorkoutSample = "workout.sample"
	TopicRaceFinished  = "race.finished"
)

// Producer publishes workout.sample/race.finished events (kafka-producer.md)
// via one shared, multi-topic *kafka-go.Writer — Topic is set per message
// rather than on the Writer itself, so both topics share one Writer and one
// Close call. Satisfies internal/room.EventPublisher structurally.
type Producer struct {
	writer *kafkago.Writer
}

// NewProducer constructs a Producer writing to brokers. Async so
// RoomActor's applyEvent/finishRace publishes never block the room's
// single-writer loop — errors surface via ErrorLogger, though WriteMessages
// can still return synchronous validation errors even in Async mode, which
// the Publish* methods below propagate to the caller for its own
// log-and-continue handling (room.go). Balancer is explicitly kafka.Hash{},
// not the Writer's default round-robin — this project's entire ordering
// guarantee (§6: same race_id key -> same partition) depends on a
// key-aware balancer; the default would silently break it.
func NewProducer(brokers []string, logger *slog.Logger) *Producer {
	return &Producer{
		writer: &kafkago.Writer{
			Addr:     kafkago.TCP(brokers...),
			Balancer: &kafkago.Hash{},
			Async:    true,
			// No out-of-band topic creation exists anywhere in this spec —
			// without this, the first publish to workout.sample/race.finished
			// would fail with "Unknown Topic Or Partition" until something
			// else created them. Relies on the broker's own default
			// auto.create.topics.enable=true.
			AllowAutoTopicCreation: true,
			ErrorLogger:            slogErrorLogger{logger: logger},
		},
	}
}

type workoutSamplePayload struct {
	RaceID    string    `json:"race_id"`
	UserID    string    `json:"user_id"`
	Ts        time.Time `json:"ts"`
	DistanceM float64   `json:"distance_m"`
	PaceWatt  float64   `json:"pace_watt"`
}

func (p *Producer) PublishWorkoutSample(ctx context.Context, raceID, userID string, ts time.Time, wordsCorrect int, paceWatt float64) error {
	value, err := json.Marshal(workoutSamplePayload{
		RaceID:    raceID,
		UserID:    userID,
		Ts:        ts,
		DistanceM: float64(wordsCorrect),
		PaceWatt:  paceWatt,
	})
	if err != nil {
		return fmt.Errorf("kafka: marshal workout sample: %w", err)
	}
	return p.writer.WriteMessages(ctx, kafkago.Message{
		Topic: TopicWorkoutSample,
		Key:   []byte(raceID),
		Value: value,
		Time:  ts,
	})
}

type raceFinishedPayload struct {
	RaceID  string                `json:"race_id"`
	Results []room.RaceResultJSON `json:"results"`
}

func (p *Producer) PublishRaceFinished(ctx context.Context, raceID string, results []room.ParticipantResult) error {
	resultsJSON := make([]room.RaceResultJSON, len(results))
	for i, res := range results {
		resultsJSON[i] = room.RaceResultJSON{
			UserID:       res.UserID,
			FinishRank:   res.FinishRank,
			FinishTimeMs: res.FinishTimeMs,
			AvgPaceWatt:  res.AvgPaceWatt,
		}
	}
	value, err := json.Marshal(raceFinishedPayload{RaceID: raceID, Results: resultsJSON})
	if err != nil {
		return fmt.Errorf("kafka: marshal race finished: %w", err)
	}
	return p.writer.WriteMessages(ctx, kafkago.Message{
		Topic: TopicRaceFinished,
		Key:   []byte(raceID),
		Value: value,
	})
}

// PublishRaw republishes an already-encoded message verbatim to topic,
// keyed by key — kafka-consumer-postgres-sink.md's dead-letter path, where
// the "payload" is whatever the original message already was (malformed
// JSON that failed to decode, or a value that decoded fine but failed to
// write for a permanent reason), not something this producer constructs
// itself the way PublishWorkoutSample/PublishRaceFinished do.
func (p *Producer) PublishRaw(ctx context.Context, topic string, key, value []byte) error {
	return p.writer.WriteMessages(ctx, kafkago.Message{
		Topic: topic,
		Key:   key,
		Value: value,
	})
}

// Close flushes and closes the underlying Writer.
func (p *Producer) Close() error {
	return p.writer.Close()
}

// slogErrorLogger adapts *slog.Logger to kafka-go's Logger interface
// (Printf(string, ...any)), wired as the Writer's ErrorLogger only — not
// Logger, which would add noisy per-batch internal chatter this project's
// structured-logging.md convention doesn't want.
type slogErrorLogger struct {
	logger *slog.Logger
}

func (l slogErrorLogger) Printf(format string, args ...interface{}) {
	l.logger.Error(fmt.Sprintf(format, args...))
}
