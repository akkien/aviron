package room

import (
	"testing"
	"time"
)

func TestRoomActor_ApplyEvent_TelemetryReceived_FinishesAtTarget(t *testing.T) {
	r := newTestActor()
	r.distanceMeters = 10
	spy := &spyFinisher{}
	r.finisher = spy

	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(TelemetryReceived{UserID: "user-1", Seq: 1, WordsCorrect: 10})

	p := r.participants["user-1"]
	if p.FinishedAt == nil {
		t.Fatal("FinishedAt is nil, want set")
	}
	if p.FinishRank == nil || *p.FinishRank != 1 {
		t.Fatalf("FinishRank = %v, want 1", p.FinishRank)
	}

	// Sole participant just finished — the race should finish too.
	if len(spy.calls) != 1 {
		t.Fatalf("finisher.calls = %d, want 1", len(spy.calls))
	}
	call := spy.calls[0]
	if call.raceID != "race-1" {
		t.Errorf("raceID = %q, want %q", call.raceID, "race-1")
	}
	if call.distanceMeters != 10 {
		t.Errorf("distanceMeters = %d, want 10", call.distanceMeters)
	}
	if len(call.results) != 1 || call.results[0].UserID != "user-1" {
		t.Fatalf("results = %+v, want one entry for user-1", call.results)
	}
	if !r.finished {
		t.Error("r.finished = false, want true after the race finishes")
	}
	select {
	case <-r.ctx.Done():
	default:
		t.Error("r.ctx not cancelled after the race finished")
	}
}

func TestRoomActor_ApplyEvent_TelemetryReceived_RankAssignedInFinishingOrder(t *testing.T) {
	r := newTestActor()
	r.distanceMeters = 10
	spy := &spyFinisher{}
	r.finisher = spy

	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(ParticipantJoined{UserID: "user-2", DisplayName: "Bob"})

	r.applyEvent(TelemetryReceived{UserID: "user-2", Seq: 1, WordsCorrect: 10}) // Bob finishes first
	if rank := r.participants["user-2"].FinishRank; rank == nil || *rank != 1 {
		t.Fatalf("user-2 FinishRank = %v, want 1", rank)
	}
	if len(spy.calls) != 0 {
		t.Fatalf("finisher called early: calls = %d, want 0 (user-1 hasn't finished)", len(spy.calls))
	}

	r.applyEvent(TelemetryReceived{UserID: "user-1", Seq: 1, WordsCorrect: 10}) // Alice finishes second
	if rank := r.participants["user-1"].FinishRank; rank == nil || *rank != 2 {
		t.Fatalf("user-1 FinishRank = %v, want 2", rank)
	}

	if len(spy.calls) != 1 {
		t.Fatalf("finisher.calls = %d, want 1 once everyone has finished", len(spy.calls))
	}
	if len(spy.calls[0].results) != 2 {
		t.Fatalf("results = %d, want 2", len(spy.calls[0].results))
	}
}

func TestRoomActor_ApplyEvent_TelemetryReceived_DoesNotFinishWhileSomeoneStillRacing(t *testing.T) {
	r := newTestActor()
	r.distanceMeters = 10
	spy := &spyFinisher{}
	r.finisher = spy

	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(ParticipantJoined{UserID: "user-2", DisplayName: "Bob"})
	r.applyEvent(TelemetryReceived{UserID: "user-1", Seq: 1, WordsCorrect: 10})

	if len(spy.calls) != 0 {
		t.Errorf("finisher.calls = %d, want 0 (user-2 hasn't finished)", len(spy.calls))
	}
	if r.finished {
		t.Error("r.finished = true, want false")
	}
}

func TestRoomActor_ApplyEvent_ParticipantLeft_EmptyingRoomTriggersFinish(t *testing.T) {
	r := newTestActor()
	spy := &spyFinisher{}
	r.finisher = spy

	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(ParticipantDisconnected{UserID: "user-1"})
	r.applyEvent(ParticipantLeft{UserID: "user-1"})

	if len(spy.calls) != 1 {
		t.Fatalf("finisher.calls = %d, want 1", len(spy.calls))
	}
	if len(spy.calls[0].results) != 0 {
		t.Errorf("results = %+v, want empty (nobody finished)", spy.calls[0].results)
	}
	if !r.finished {
		t.Error("r.finished = false, want true")
	}
}

func TestRoomActor_ApplyEvent_ParticipantLeft_DoesNotFinishIfOthersStillRacing(t *testing.T) {
	r := newTestActor()
	spy := &spyFinisher{}
	r.finisher = spy

	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(ParticipantJoined{UserID: "user-2", DisplayName: "Bob"})
	r.applyEvent(ParticipantDisconnected{UserID: "user-1"})
	r.applyEvent(ParticipantLeft{UserID: "user-1"})

	if len(spy.calls) != 0 {
		t.Errorf("finisher.calls = %d, want 0 (user-2 is still racing)", len(spy.calls))
	}
}

func TestRoomActor_ApplyEvent_NoShowTimeout_EmptyRoomTriggersFinish(t *testing.T) {
	r := newTestActor()
	spy := &spyFinisher{}
	r.finisher = spy

	r.applyEvent(noShowTimeout{})

	if len(spy.calls) != 1 {
		t.Fatalf("finisher.calls = %d, want 1", len(spy.calls))
	}
	if len(spy.calls[0].results) != 0 {
		t.Errorf("results = %+v, want empty", spy.calls[0].results)
	}
}

func TestRoomActor_ApplyEvent_NoShowTimeout_NoopIfSomeoneJoined(t *testing.T) {
	r := newTestActor()
	spy := &spyFinisher{}
	r.finisher = spy

	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(noShowTimeout{})

	if len(spy.calls) != 0 {
		t.Errorf("finisher.calls = %d, want 0 (user-1 is still racing, hasn't finished)", len(spy.calls))
	}
}

func TestRoomActor_CheckRaceFinished_DoesNotCallFinisherTwice(t *testing.T) {
	r := newTestActor()
	spy := &spyFinisher{}
	r.finisher = spy

	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(ParticipantDisconnected{UserID: "user-1"})
	r.applyEvent(ParticipantLeft{UserID: "user-1"})
	if len(spy.calls) != 1 {
		t.Fatalf("finisher.calls = %d, want 1 after first finish", len(spy.calls))
	}

	// Simulate a stray late event reaching checkRaceFinished again (e.g. a
	// duplicate noShowTimeout firing after the race already finished).
	r.checkRaceFinished()

	if len(spy.calls) != 1 {
		t.Errorf("finisher.calls = %d after a second checkRaceFinished, want still 1", len(spy.calls))
	}
}

func TestRoomActor_FinishRace_ComputesFinishTimeMsFromStartedAt(t *testing.T) {
	r := newTestActor()
	r.distanceMeters = 10
	r.startedAt = time.Now().Add(-5 * time.Second)
	spy := &spyFinisher{}
	r.finisher = spy

	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(TelemetryReceived{UserID: "user-1", Seq: 1, WordsCorrect: 10})

	if len(spy.calls) != 1 {
		t.Fatalf("finisher.calls = %d, want 1", len(spy.calls))
	}
	result := spy.calls[0].results[0]
	if result.FinishTimeMs == nil {
		t.Fatal("FinishTimeMs is nil, want set")
	}
	// startedAt was backdated 5s; allow generous slack for test execution time.
	if *result.FinishTimeMs < 4900 || *result.FinishTimeMs > 6000 {
		t.Errorf("FinishTimeMs = %d, want roughly 5000", *result.FinishTimeMs)
	}
}

func TestRoomActor_FinishRace_BroadcastsRaceFinishedMessage(t *testing.T) {
	r := newTestActor()
	r.distanceMeters = 10
	r.broadcast = make(chan []byte, 8)
	spy := &spyFinisher{}
	r.finisher = spy

	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	<-r.broadcast // drain the immediate join snapshot

	r.applyEvent(TelemetryReceived{UserID: "user-1", Seq: 1, WordsCorrect: 10})

	select {
	case body := <-r.broadcast:
		if len(body) == 0 {
			t.Error("race_finished broadcast body is empty")
		}
	default:
		t.Fatal("no race_finished message broadcast")
	}
}
