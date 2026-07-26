package consumer

import (
	"context"
	"errors"
	"log/slog"

	kafkago "github.com/segmentio/kafka-go"
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
	decoded, decodeErr := decodeRaceFinished(msg.Value)
	if decodeErr != nil {
		c.logger.Warn("dropping malformed race finished message", slog.Any("error", decodeErr))
		c.deadLetter(ctx, raceFinishedDLQTopic, msg)
		if cerr := reader.CommitMessages(ctx, msg); cerr != nil {
			c.logger.Error("commit malformed race finished message failed", slog.Any("error", cerr))
		}
		return
	}

	if err := c.reconciler.ReconcileParticipantResults(ctx, decoded.RaceID, decoded.Results); err != nil {
		if errors.Is(err, ErrPermanentWrite) {
			c.logger.Warn("dead-lettering race finished message", slog.Any("error", err))
			c.deadLetter(ctx, raceFinishedDLQTopic, msg)
			if cerr := reader.CommitMessages(ctx, msg); cerr != nil {
				c.logger.Error("commit dead-lettered race finished message failed", slog.Any("error", cerr))
			}
			return
		}
		// Transient — do not commit, let redelivery retry.
		c.logger.Error("reconcile race finished failed", slog.Any("error", err))
		return
	}

	if cerr := reader.CommitMessages(ctx, msg); cerr != nil {
		c.logger.Error("commit race finished message failed", slog.Any("error", cerr))
	}
}
