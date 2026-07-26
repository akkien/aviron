package consumer

import (
	"context"
	"errors"
	"fmt"
	"testing"

	kafkago "github.com/segmentio/kafka-go"
)

func TestProcessRaceFinishedMessage_MalformedJSON_DeadLettersAndCommits(t *testing.T) {
	reconciler := &fakeReconciler{}
	dlq := &fakeDLQ{}
	c := newTestConsumer(&fakeWriter{}, reconciler, dlq)
	committer := &fakeCommitter{}
	msg := kafkago.Message{Value: []byte(`not json`)}

	c.processRaceFinishedMessage(context.Background(), committer, msg)

	if len(reconciler.calls) != 0 {
		t.Fatalf("expected no reconcile call for a malformed message, got %d", len(reconciler.calls))
	}
	if len(dlq.calls) != 1 || dlq.calls[0].topic != raceFinishedDLQTopic {
		t.Fatalf("expected one dead-letter to %q, got %+v", raceFinishedDLQTopic, dlq.calls)
	}
	if len(committer.committed) != 1 {
		t.Fatal("expected the malformed message's offset to be committed")
	}
}

func TestProcessRaceFinishedMessage_Success_ReconcilesAndCommits(t *testing.T) {
	reconciler := &fakeReconciler{}
	c := newTestConsumer(&fakeWriter{}, reconciler, &fakeDLQ{})
	committer := &fakeCommitter{}
	msg := kafkago.Message{Value: []byte(`{"race_id":"race-1","results":[{"user_id":"user-1","finish_rank":1,"avg_pace_watt":50}]}`)}

	c.processRaceFinishedMessage(context.Background(), committer, msg)

	if len(reconciler.calls) != 1 || reconciler.calls[0].raceID != "race-1" {
		t.Fatalf("expected one reconcile call for race-1, got %+v", reconciler.calls)
	}
	if len(committer.committed) != 1 {
		t.Fatal("expected the message to be committed after a successful reconcile")
	}
}

func TestProcessRaceFinishedMessage_PermanentError_DeadLettersAndCommits(t *testing.T) {
	reconciler := &fakeReconciler{err: fmt.Errorf("wrapped: %w", ErrPermanentWrite)}
	dlq := &fakeDLQ{}
	c := newTestConsumer(&fakeWriter{}, reconciler, dlq)
	committer := &fakeCommitter{}
	msg := kafkago.Message{Value: []byte(`{"race_id":"race-1","results":[]}`)}

	c.processRaceFinishedMessage(context.Background(), committer, msg)

	if len(dlq.calls) != 1 || dlq.calls[0].topic != raceFinishedDLQTopic {
		t.Fatalf("expected one dead-letter to %q, got %+v", raceFinishedDLQTopic, dlq.calls)
	}
	if len(committer.committed) != 1 {
		t.Fatal("expected the dead-lettered message's offset to be committed")
	}
}

func TestProcessRaceFinishedMessage_TransientError_NoCommitNoDeadLetter(t *testing.T) {
	reconciler := &fakeReconciler{err: errors.New("connection reset")}
	dlq := &fakeDLQ{}
	c := newTestConsumer(&fakeWriter{}, reconciler, dlq)
	committer := &fakeCommitter{}
	msg := kafkago.Message{Value: []byte(`{"race_id":"race-1","results":[]}`)}

	c.processRaceFinishedMessage(context.Background(), committer, msg)

	if len(dlq.calls) != 0 {
		t.Fatalf("expected no dead-lettering for a transient error, got %d", len(dlq.calls))
	}
	if len(committer.committed) != 0 {
		t.Fatal("expected no commit for a transient error, so it redelivers")
	}
}
