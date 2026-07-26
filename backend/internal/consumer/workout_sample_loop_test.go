package consumer

import (
	"context"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

func TestFlushWorkoutSampleBatch_Empty_NoOp(t *testing.T) {
	writer := &fakeWriter{}
	c := newTestConsumer(writer, &fakeReconciler{}, &fakeDLQ{})
	committer := &fakeCommitter{}
	batch := &sampleBatch{}

	c.flushWorkoutSampleBatch(context.Background(), committer, batch)

	if len(writer.inserted) != 0 {
		t.Fatalf("expected InsertBatch not to be called for an empty batch, got %d calls", len(writer.inserted))
	}
	if len(committer.committed) != 0 {
		t.Fatalf("expected no commits for an empty batch, got %d", len(committer.committed))
	}
}

func TestFlushWorkoutSampleBatch_Success_CommitsAndResets(t *testing.T) {
	writer := &fakeWriter{}
	c := newTestConsumer(writer, &fakeReconciler{}, &fakeDLQ{})
	committer := &fakeCommitter{}
	batch := &sampleBatch{}
	msg1 := kafkago.Message{Offset: 1}
	msg2 := kafkago.Message{Offset: 2}
	now := time.Now()
	batch.add(WorkoutSample{RaceID: "race-1"}, msg1, now)
	batch.add(WorkoutSample{RaceID: "race-1"}, msg2, now)

	c.flushWorkoutSampleBatch(context.Background(), committer, batch)

	if len(writer.inserted) != 1 || len(writer.inserted[0]) != 2 {
		t.Fatalf("expected one InsertBatch call with 2 samples, got %+v", writer.inserted)
	}
	if len(committer.committed) != 2 {
		t.Fatalf("expected both messages committed, got %d", len(committer.committed))
	}
	if len(batch.samples) != 0 || len(batch.messages) != 0 {
		t.Fatal("expected batch to be reset after a successful flush")
	}
}

func TestFlushWorkoutSampleBatch_PermanentError_DeadLettersCommitsAndResets(t *testing.T) {
	writer := &fakeWriter{err: ErrPermanentWrite}
	dlq := &fakeDLQ{}
	c := newTestConsumer(writer, &fakeReconciler{}, dlq)
	committer := &fakeCommitter{}
	batch := &sampleBatch{}
	msg := kafkago.Message{Offset: 1, Key: []byte("race-1"), Value: []byte(`{}`)}
	batch.add(WorkoutSample{RaceID: "race-1"}, msg, time.Now())

	c.flushWorkoutSampleBatch(context.Background(), committer, batch)

	if len(dlq.calls) != 1 {
		t.Fatalf("expected the message to be dead-lettered, got %d DLQ calls", len(dlq.calls))
	}
	if dlq.calls[0].topic != workoutSampleDLQTopic {
		t.Fatalf("got DLQ topic %q, want %q", dlq.calls[0].topic, workoutSampleDLQTopic)
	}
	if len(committer.committed) != 1 {
		t.Fatalf("expected the dead-lettered message's offset to still be committed, got %d commits", len(committer.committed))
	}
	if len(batch.samples) != 0 {
		t.Fatal("expected batch to be reset after dead-lettering")
	}
}

func TestFlushWorkoutSampleBatch_TransientError_NoCommitNoReset(t *testing.T) {
	writer := &fakeWriter{err: context.DeadlineExceeded} // stand-in for a transient connection error
	dlq := &fakeDLQ{}
	c := newTestConsumer(writer, &fakeReconciler{}, dlq)
	committer := &fakeCommitter{}
	batch := &sampleBatch{}
	msg := kafkago.Message{Offset: 1}
	batch.add(WorkoutSample{RaceID: "race-1"}, msg, time.Now())

	c.flushWorkoutSampleBatch(context.Background(), committer, batch)

	if len(dlq.calls) != 0 {
		t.Fatalf("expected no dead-lettering for a transient error, got %d DLQ calls", len(dlq.calls))
	}
	if len(committer.committed) != 0 {
		t.Fatalf("expected no commit for a transient error, got %d commits", len(committer.committed))
	}
	if len(batch.samples) != 1 {
		t.Fatal("expected the batch to survive a transient error, ready to retry")
	}
}
