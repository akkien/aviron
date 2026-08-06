package consumer

import (
	"context"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"

	"github.com/akkien/aviron/internal/kafka"
)

func TestFlushWorkoutSampleBatch_Success_ObservesBatchInsertMetric(t *testing.T) {
	spy := &spyMetrics{}
	c := newTestConsumerWithMetrics(&fakeWriter{}, &fakeReconciler{}, &fakeDLQ{}, spy)
	batch := &sampleBatch{}
	batch.add(WorkoutSample{RaceID: "race-1"}, kafkago.Message{Offset: 1}, time.Now())

	c.flushWorkoutSampleBatch(context.Background(), &fakeCommitter{}, batch)

	if len(spy.batchInserts) != 1 {
		t.Fatalf("expected one ObserveBatchInsert call, got %d", len(spy.batchInserts))
	}
	if spy.batchInserts[0].topic != kafka.TopicWorkoutSample || spy.batchInserts[0].err != nil {
		t.Fatalf("got %+v, want topic=%q err=nil", spy.batchInserts[0], kafka.TopicWorkoutSample)
	}
}

func TestFlushWorkoutSampleBatch_TransientError_ObservesBatchInsertMetricWithError(t *testing.T) {
	spy := &spyMetrics{}
	c := newTestConsumerWithMetrics(&fakeWriter{err: context.DeadlineExceeded}, &fakeReconciler{}, &fakeDLQ{}, spy)
	batch := &sampleBatch{}
	batch.add(WorkoutSample{RaceID: "race-1"}, kafkago.Message{Offset: 1}, time.Now())

	c.flushWorkoutSampleBatch(context.Background(), &fakeCommitter{}, batch)

	if len(spy.batchInserts) != 1 || spy.batchInserts[0].err == nil {
		t.Fatalf("expected one ObserveBatchInsert call with a non-nil error, got %+v", spy.batchInserts)
	}
}

func TestDeadLetter_IncrementsDLQMetric(t *testing.T) {
	spy := &spyMetrics{}
	c := newTestConsumerWithMetrics(&fakeWriter{}, &fakeReconciler{}, &fakeDLQ{}, spy)

	c.deadLetter(context.Background(), workoutSampleDLQTopic, kafkago.Message{Key: []byte("k"), Value: []byte("v")})

	if len(spy.dlqTopics) != 1 || spy.dlqTopics[0] != workoutSampleDLQTopic {
		t.Fatalf("got %v, want a single call with topic %q", spy.dlqTopics, workoutSampleDLQTopic)
	}
}

func TestProcessRaceFinishedMessage_Success_ObservesBatchInsertMetric(t *testing.T) {
	spy := &spyMetrics{}
	c := newTestConsumerWithMetrics(&fakeWriter{}, &fakeReconciler{}, &fakeDLQ{}, spy)
	msg := kafkago.Message{Value: []byte(`{"race_id":"race-1","results":[{"user_id":"user-1","finish_rank":1,"avg_pace_watt":50}]}`)}

	c.processRaceFinishedMessage(context.Background(), &fakeCommitter{}, msg)

	if len(spy.batchInserts) != 1 {
		t.Fatalf("expected one ObserveBatchInsert call, got %d", len(spy.batchInserts))
	}
	if spy.batchInserts[0].topic != kafka.TopicRaceFinished || spy.batchInserts[0].err != nil {
		t.Fatalf("got %+v, want topic=%q err=nil", spy.batchInserts[0], kafka.TopicRaceFinished)
	}
}

func TestConsumer_Lag_ZeroBeforeReadersAreSet(t *testing.T) {
	c := newTestConsumer(&fakeWriter{}, &fakeReconciler{}, &fakeDLQ{})

	workoutSample, raceFinished := c.Lag()
	if workoutSample != 0 || raceFinished != 0 {
		t.Fatalf("Lag() = (%d, %d), want (0, 0) before Run has constructed any reader", workoutSample, raceFinished)
	}
}

func TestConsumer_Lag_ReadsStoredReaderStats(t *testing.T) {
	c := newTestConsumer(&fakeWriter{}, &fakeReconciler{}, &fakeDLQ{})

	reader := kafkago.NewReader(kafkago.ReaderConfig{Brokers: []string{"localhost:9092"}, Topic: kafka.TopicWorkoutSample})
	t.Cleanup(func() { reader.Close() })
	c.workoutReader.Store(reader)

	workoutSample, raceFinished := c.Lag()
	if workoutSample != reader.Stats().Lag {
		t.Fatalf("Lag() workoutSample = %d, want %d (reader's own Stats().Lag)", workoutSample, reader.Stats().Lag)
	}
	if raceFinished != 0 {
		t.Fatalf("Lag() raceFinished = %d, want 0 — race-finished reader was never stored", raceFinished)
	}
}
