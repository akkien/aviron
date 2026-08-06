package consumer

import (
	"context"
	"errors"
	"log/slog"
	"time"

	kafkago "github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/akkien/aviron/internal/kafka"
	"github.com/akkien/aviron/internal/tracing"
)

// runRaceFinishedLoop has no batching — race.finished volume is orders of
// magnitude lower than workout.sample (one message per race), so each
// message is reconciled and committed immediately.
func (c *Consumer) runRaceFinishedLoop(ctx context.Context, reader *kafkago.Reader) {
	defer reader.Close()

	for {
		msg, err := reader.FetchMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.logger.Error("fetch race finished failed", slog.Any("error", err))
			continue
		}

		c.processRaceFinishedMessage(ctx, reader, msg)
	}
}

// processRaceFinishedMessage decodes, reconciles, and commits (or
// dead-letters) one message — pulled out of runRaceFinishedLoop's fetch
// loop so it's unit-testable against a fake committer/reconciler/dlq,
// without a real Kafka broker.
func (c *Consumer) processRaceFinishedMessage(ctx context.Context, reader committer, msg kafkago.Message) {
	// kafka.consume lands in the same trace as the publisher's kafka.produce
	// span (RoomActor.finishRace's PublishRaceFinished) — race.finished has
	// no batching (one message per race), so unlike workout.sample this span
	// covers the whole decode/reconcile/commit path, not just decode
	// (tracing/instrumentation.md).
	msgCtx := otel.GetTextMapPropagator().Extract(context.Background(), kafka.HeaderCarrier{Headers: &msg.Headers})
	ctx, span := tracer.Start(msgCtx, "kafka.consume", trace.WithAttributes(
		attribute.String("topic", kafka.TopicRaceFinished),
		attribute.String("group_id", raceFinishedGroupID),
	))
	defer span.End()

	decoded, decodeErr := decodeRaceFinished(msg.Value)
	if decodeErr != nil {
		span.RecordError(decodeErr)
		c.logger.Warn("dropping malformed race finished message", append([]any{slog.Any("error", decodeErr)}, tracing.LogAttrs(ctx)...)...)
		c.deadLetter(ctx, raceFinishedDLQTopic, msg)
		if cerr := reader.CommitMessages(ctx, msg); cerr != nil {
			c.logger.Error("commit malformed race finished message failed", append([]any{slog.Any("error", cerr)}, tracing.LogAttrs(ctx)...)...)
		}
		return
	}

	start := time.Now()
	err := c.reconciler.ReconcileParticipantResults(ctx, decoded.RaceID, decoded.Results)
	c.metrics.ObserveBatchInsert(kafka.TopicRaceFinished, time.Since(start), err)
	if err != nil {
		span.RecordError(err)
		if errors.Is(err, ErrPermanentWrite) {
			c.logger.Warn("dead-lettering race finished message", append([]any{slog.Any("error", err)}, tracing.LogAttrs(ctx)...)...)
			c.deadLetter(ctx, raceFinishedDLQTopic, msg)
			if cerr := reader.CommitMessages(ctx, msg); cerr != nil {
				c.logger.Error("commit dead-lettered race finished message failed", append([]any{slog.Any("error", cerr)}, tracing.LogAttrs(ctx)...)...)
			}
			return
		}
		// Transient — do not commit, let redelivery retry.
		c.logger.Error("reconcile race finished failed", append([]any{slog.Any("error", err)}, tracing.LogAttrs(ctx)...)...)
		return
	}

	if cerr := reader.CommitMessages(ctx, msg); cerr != nil {
		c.logger.Error("commit race finished message failed", append([]any{slog.Any("error", cerr)}, tracing.LogAttrs(ctx)...)...)
	}
}
