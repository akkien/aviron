package consumer

import (
	"context"
	"errors"
	"log/slog"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// committer is the one *kafka.Reader method flushWorkoutSampleBatch
// actually needs — narrowed to an interface so the flush/DLQ-routing
// decision logic is unit-testable against a fake, without a real Kafka
// broker (kafka-consumer-postgres-sink.md's own Testing section calls for
// this; *kafkago.Reader has no lightweight fake of its own the way
// miniredis stands in for Redis elsewhere in this codebase).
type committer interface {
	CommitMessages(ctx context.Context, msgs ...kafkago.Message) error
}

// runWorkoutSampleLoop owns reader and batch entirely within this one
// goroutine — the single-writer-per-goroutine shape this codebase already
// uses everywhere else (room actors, hub fan-out), applied to a Kafka
// consumer loop instead.
func (c *Consumer) runWorkoutSampleLoop(ctx context.Context, reader *kafkago.Reader) {
	defer reader.Close()

	batch := &sampleBatch{}

	for {
		if ctx.Err() != nil {
			c.flushWorkoutSampleBatch(context.Background(), reader, batch)
			return
		}

		fetchCtx, cancel := context.WithTimeout(ctx, fetchPollTimeout)
		msg, err := reader.FetchMessage(fetchCtx)
		cancel()

		switch {
		case err == nil:
			sample, decodeErr := decodeWorkoutSample(msg.Value)
			if decodeErr != nil {
				// A malformed message will never become parseable by
				// retrying it — dead-letter it and commit immediately so
				// it doesn't crash-loop the consumer on the same offset.
				c.logger.Warn("dropping malformed workout sample", slog.Any("error", decodeErr))
				c.deadLetter(ctx, workoutSampleDLQTopic, msg)
				if cerr := reader.CommitMessages(ctx, msg); cerr != nil {
					c.logger.Error("commit malformed workout sample failed", slog.Any("error", cerr))
				}
				continue
			}
			batch.add(sample, msg, time.Now())
		case errors.Is(err, context.DeadlineExceeded):
			// No message within this poll window — fall through to the
			// flush check below; not a real error.
		case ctx.Err() != nil:
			c.flushWorkoutSampleBatch(context.Background(), reader, batch)
			return
		default:
			c.logger.Error("fetch workout sample failed", slog.Any("error", err))
			continue
		}

		if batch.shouldFlush(time.Now()) {
			c.flushWorkoutSampleBatch(ctx, reader, batch)
		}
	}
}

// flushWorkoutSampleBatch writes the batch and commits its messages'
// offsets only on success or a permanent (dead-lettered) failure — a
// transient failure leaves the batch and its offsets untouched so the next
// flush attempt retries the same accumulated data, naturally throttled by
// the loop's own poll cadence rather than a tight retry spin.
func (c *Consumer) flushWorkoutSampleBatch(ctx context.Context, reader committer, batch *sampleBatch) {
	if len(batch.samples) == 0 {
		return
	}

	err := c.writer.InsertBatch(ctx, batch.samples)
	if err == nil {
		if cerr := reader.CommitMessages(ctx, batch.messages...); cerr != nil {
			c.logger.Error("commit workout sample batch failed", slog.Any("error", cerr))
		}
		batch.reset()
		return
	}

	if errors.Is(err, ErrPermanentWrite) {
		c.logger.Warn("dead-lettering workout sample batch", slog.Any("error", err))
		for _, msg := range batch.messages {
			c.deadLetter(ctx, workoutSampleDLQTopic, msg)
		}
		if cerr := reader.CommitMessages(ctx, batch.messages...); cerr != nil {
			c.logger.Error("commit dead-lettered workout sample batch failed", slog.Any("error", cerr))
		}
		batch.reset()
		return
	}

	c.logger.Error("insert workout sample batch failed", slog.Any("error", err))
}
