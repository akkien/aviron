package consumer

import (
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// sampleBatch accumulates WorkoutSamples, paired with the raw kafka.Message
// each came from (needed to commit offsets once the batch is durably
// written), and decides when to flush — pure state, no network I/O of its
// own, so it's directly unit-testable without a real Kafka broker or
// Postgres. Flushes on whichever of flushBatchSize/flushInterval comes
// first (project-overview.md §3), so a burst of activity doesn't wait the
// full window and a quiet period doesn't hold a half-empty batch forever.
type sampleBatch struct {
	samples  []WorkoutSample
	messages []kafkago.Message
	openedAt time.Time
}

func (b *sampleBatch) add(s WorkoutSample, msg kafkago.Message, now time.Time) {
	if len(b.samples) == 0 {
		b.openedAt = now
	}
	b.samples = append(b.samples, s)
	b.messages = append(b.messages, msg)
}

func (b *sampleBatch) shouldFlush(now time.Time) bool {
	if len(b.samples) == 0 {
		return false
	}
	return len(b.samples) >= flushBatchSize || now.Sub(b.openedAt) >= flushInterval
}

func (b *sampleBatch) reset() {
	b.samples = nil
	b.messages = nil
}
