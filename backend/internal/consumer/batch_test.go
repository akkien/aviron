package consumer

import (
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

func TestSampleBatch_ShouldFlush_Empty(t *testing.T) {
	b := &sampleBatch{}
	if b.shouldFlush(time.Now()) {
		t.Fatal("expected empty batch to never flush")
	}
}

func TestSampleBatch_ShouldFlush_SizeThreshold(t *testing.T) {
	b := &sampleBatch{}
	now := time.Now()
	for i := 0; i < flushBatchSize-1; i++ {
		b.add(WorkoutSample{}, kafkago.Message{}, now)
		if b.shouldFlush(now) {
			t.Fatalf("expected no flush before reaching flushBatchSize, got one at %d samples", i+1)
		}
	}
	b.add(WorkoutSample{}, kafkago.Message{}, now)
	if !b.shouldFlush(now) {
		t.Fatal("expected flush once flushBatchSize is reached")
	}
}

func TestSampleBatch_ShouldFlush_TimeThreshold(t *testing.T) {
	b := &sampleBatch{}
	opened := time.Now()
	b.add(WorkoutSample{RaceID: "race-1"}, kafkago.Message{}, opened)

	if b.shouldFlush(opened.Add(flushInterval - time.Millisecond)) {
		t.Fatal("expected no flush before flushInterval elapses")
	}
	if !b.shouldFlush(opened.Add(flushInterval)) {
		t.Fatal("expected flush once flushInterval elapses")
	}
}

func TestSampleBatch_Add_OnlySetsOpenedAtOnFirstSample(t *testing.T) {
	b := &sampleBatch{}
	first := time.Now()
	second := first.Add(time.Hour)

	b.add(WorkoutSample{RaceID: "race-1"}, kafkago.Message{}, first)
	b.add(WorkoutSample{RaceID: "race-2"}, kafkago.Message{}, second)

	if !b.openedAt.Equal(first) {
		t.Fatalf("expected openedAt to stay pinned to the first add, got %v", b.openedAt)
	}
	if len(b.samples) != 2 || len(b.messages) != 2 {
		t.Fatalf("expected 2 samples/messages, got %d/%d", len(b.samples), len(b.messages))
	}
}

func TestSampleBatch_Reset_ClearsSamplesAndMessagesAndReopens(t *testing.T) {
	b := &sampleBatch{}
	now := time.Now()
	b.add(WorkoutSample{RaceID: "race-1"}, kafkago.Message{}, now)

	b.reset()

	if len(b.samples) != 0 || len(b.messages) != 0 {
		t.Fatalf("expected reset to clear samples/messages, got %d/%d", len(b.samples), len(b.messages))
	}
	if b.shouldFlush(now) {
		t.Fatal("expected a freshly reset batch to never flush")
	}

	// A sample added after reset should re-open the window from its own
	// timestamp, not the original openedAt.
	later := now.Add(time.Hour)
	b.add(WorkoutSample{RaceID: "race-2"}, kafkago.Message{}, later)
	if !b.openedAt.Equal(later) {
		t.Fatalf("expected openedAt to reopen at %v, got %v", later, b.openedAt)
	}
}
