package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	kafkago "github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/akkien/aviron/internal/room"
)

// tracer is this package's own OpenTelemetry tracer (tracing/instrumentation.md).
var tracer = otel.Tracer("github.com/akkien/aviron/internal/kafka")

// HeaderCarrier adapts a *[]kafkago.Header to propagation.TextMapCarrier, so
// the current span's trace context can ride in a kafkago.Message's own
// Headers field — unlike NATS, no structural change to the message type
// itself was needed, kafkago.Message already carries Headers. Shared with
// internal/consumer's own extraction on the receiving side, so both ends of
// this hop agree on the same header encoding without duplicating it.
type HeaderCarrier struct {
	Headers *[]kafkago.Header
}

func (c HeaderCarrier) Get(key string) string {
	for _, h := range *c.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

func (c HeaderCarrier) Set(key, value string) {
	for i, h := range *c.Headers {
		if h.Key == key {
			(*c.Headers)[i].Value = []byte(value)
			return
		}
	}
	*c.Headers = append(*c.Headers, kafkago.Header{Key: key, Value: []byte(value)})
}

func (c HeaderCarrier) Keys() []string {
	keys := make([]string, len(*c.Headers))
	for i, h := range *c.Headers {
		keys[i] = h.Key
	}
	return keys
}

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
	ctx, span := tracer.Start(ctx, "kafka.produce", trace.WithAttributes(
		attribute.String("topic", TopicWorkoutSample),
		attribute.String("race_id", raceID),
	))
	defer span.End()

	value, err := json.Marshal(workoutSamplePayload{
		RaceID:    raceID,
		UserID:    userID,
		Ts:        ts,
		DistanceM: float64(wordsCorrect),
		PaceWatt:  paceWatt,
	})
	if err != nil {
		span.RecordError(err)
		return fmt.Errorf("kafka: marshal workout sample: %w", err)
	}

	var headers []kafkago.Header
	otel.GetTextMapPropagator().Inject(ctx, HeaderCarrier{Headers: &headers})
	if err := p.writer.WriteMessages(ctx, kafkago.Message{
		Topic:   TopicWorkoutSample,
		Key:     []byte(raceID),
		Value:   value,
		Time:    ts,
		Headers: headers,
	}); err != nil {
		span.RecordError(err)
		return err
	}
	return nil
}

type raceFinishedPayload struct {
	RaceID  string                `json:"race_id"`
	Results []room.RaceResultJSON `json:"results"`
}

func (p *Producer) PublishRaceFinished(ctx context.Context, raceID string, results []room.ParticipantResult) error {
	ctx, span := tracer.Start(ctx, "kafka.produce", trace.WithAttributes(
		attribute.String("topic", TopicRaceFinished),
		attribute.String("race_id", raceID),
	))
	defer span.End()

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
		span.RecordError(err)
		return fmt.Errorf("kafka: marshal race finished: %w", err)
	}

	var headers []kafkago.Header
	otel.GetTextMapPropagator().Inject(ctx, HeaderCarrier{Headers: &headers})
	if err := p.writer.WriteMessages(ctx, kafkago.Message{
		Topic:   TopicRaceFinished,
		Key:     []byte(raceID),
		Value:   value,
		Headers: headers,
	}); err != nil {
		span.RecordError(err)
		return err
	}
	return nil
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
