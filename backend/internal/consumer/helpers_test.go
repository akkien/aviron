package consumer

import (
	"context"
	"log/slog"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/akkien/aviron/internal/room"
)

var testLogger = slog.New(slog.DiscardHandler)

// fakeWriter/fakeReconciler/fakeDLQ/fakeCommitter are in-memory fakes for
// internal/consumer's four collaborator interfaces — this package isn't
// REST-layered, but the "test against a fake, not real infra" testability
// rationale coding-standards.md states for REST-domain Repository
// interfaces applies the same way here (kafka-consumer-postgres-sink.md's
// own Testing section calls for exactly this).

type fakeWriter struct {
	inserted [][]WorkoutSample
	err      error
}

func (f *fakeWriter) InsertBatch(ctx context.Context, samples []WorkoutSample) error {
	if f.err != nil {
		return f.err
	}
	cp := make([]WorkoutSample, len(samples))
	copy(cp, samples)
	f.inserted = append(f.inserted, cp)
	return nil
}

type reconcileCall struct {
	raceID  string
	results []room.RaceResultJSON
}

type fakeReconciler struct {
	calls []reconcileCall
	err   error
}

func (f *fakeReconciler) ReconcileParticipantResults(ctx context.Context, raceID string, results []room.RaceResultJSON) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, reconcileCall{raceID: raceID, results: results})
	return nil
}

type dlqCall struct {
	topic string
	key   []byte
	value []byte
}

type fakeDLQ struct {
	calls []dlqCall
	err   error
}

func (f *fakeDLQ) PublishRaw(ctx context.Context, topic string, key, value []byte) error {
	f.calls = append(f.calls, dlqCall{topic: topic, key: key, value: value})
	return f.err
}

type fakeCommitter struct {
	committed []kafkago.Message
	err       error
}

func (f *fakeCommitter) CommitMessages(ctx context.Context, msgs ...kafkago.Message) error {
	if f.err != nil {
		return f.err
	}
	f.committed = append(f.committed, msgs...)
	return nil
}

func newTestConsumer(writer WorkoutSampleWriter, reconciler FinishReconciler, dlq DLQPublisher) *Consumer {
	return NewConsumer([]string{"localhost:9092"}, writer, reconciler, dlq, testLogger)
}
