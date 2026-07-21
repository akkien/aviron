package room

import (
	"fmt"
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

func TestRoomActor_ApplyEvent_TelemetryReceived_RankUnaffectedByEarlierFinisherDeparting(t *testing.T) {
	// Regression: FinishRank must not be derived by counting FinishRank !=
	// nil over the live r.participants map at the moment someone finishes —
	// a participant who already finished can later depart (their connection
	// drops and the grace period lapses) and move into departedParticipants,
	// which would silently drop them from that count and hand the next
	// finisher a duplicate rank (leave-race.md).
	r := newTestActor()
	r.distanceMeters = 10
	spy := &spyFinisher{}
	r.finisher = spy

	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(ParticipantJoined{UserID: "user-2", DisplayName: "Bob"})
	r.applyEvent(ParticipantJoined{UserID: "user-3", DisplayName: "Carol"})

	r.applyEvent(TelemetryReceived{UserID: "user-1", Seq: 1, WordsCorrect: 10}) // user-1 finishes rank 1

	// user-1 disconnects after finishing and their grace period expires —
	// moved out of r.participants into departedParticipants, but their
	// FinishRank must still count toward the next finisher's rank.
	r.applyEvent(ParticipantDisconnected{UserID: "user-1"})
	r.applyEvent(ParticipantEvicted{UserID: "user-1"})

	r.applyEvent(TelemetryReceived{UserID: "user-2", Seq: 1, WordsCorrect: 10}) // user-2 finishes rank 2
	if rank := r.participants["user-2"].FinishRank; rank == nil || *rank != 2 {
		t.Fatalf("user-2 FinishRank = %v, want 2 (user-1 already finished at rank 1, despite having since departed)", rank)
	}

	r.applyEvent(TelemetryReceived{UserID: "user-3", Seq: 1, WordsCorrect: 10}) // user-3 finishes rank 3
	if rank := r.participants["user-3"].FinishRank; rank == nil || *rank != 3 {
		t.Fatalf("user-3 FinishRank = %v, want 3", rank)
	}

	if len(spy.calls) != 1 {
		t.Fatalf("finisher.calls = %d, want 1", len(spy.calls))
	}
	ranks := make(map[string]int)
	for _, res := range spy.calls[0].results {
		if res.FinishRank != nil {
			ranks[res.UserID] = *res.FinishRank
		}
	}
	if ranks["user-1"] != 1 || ranks["user-2"] != 2 || ranks["user-3"] != 3 {
		t.Errorf("ranks = %+v, want user-1:1 user-2:2 user-3:3 (no duplicates)", ranks)
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

func TestRoomActor_ApplyEvent_ParticipantEvicted_EmptyingRoomTriggersFinish(t *testing.T) {
	r := newTestActor()
	spy := &spyFinisher{}
	r.finisher = spy

	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(ParticipantDisconnected{UserID: "user-1"})
	r.applyEvent(ParticipantEvicted{UserID: "user-1"})

	if len(spy.calls) != 1 {
		t.Fatalf("finisher.calls = %d, want 1", len(spy.calls))
	}
	// leave-race.md: a non-finisher no longer vanishes from the results —
	// they get a result with a shared last-place rank equal to the total
	// number of distinct participants the room ever saw (1, here).
	if len(spy.calls[0].results) != 1 {
		t.Fatalf("results = %+v, want 1 entry for the evicted non-finisher", spy.calls[0].results)
	}
	result := spy.calls[0].results[0]
	if result.UserID != "user-1" {
		t.Errorf("UserID = %q, want %q", result.UserID, "user-1")
	}
	if result.FinishRank == nil || *result.FinishRank != 1 {
		t.Errorf("FinishRank = %v, want 1 (shared last place, total 1 participant ever joined)", result.FinishRank)
	}
	if result.FinishTimeMs != nil {
		t.Errorf("FinishTimeMs = %v, want nil (never finished)", result.FinishTimeMs)
	}
	if !r.finished {
		t.Error("r.finished = false, want true")
	}
}

func TestRoomActor_ApplyEvent_ParticipantEvicted_DoesNotFinishIfOthersStillRacing(t *testing.T) {
	r := newTestActor()
	spy := &spyFinisher{}
	r.finisher = spy

	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(ParticipantJoined{UserID: "user-2", DisplayName: "Bob"})
	r.applyEvent(ParticipantDisconnected{UserID: "user-1"})
	r.applyEvent(ParticipantEvicted{UserID: "user-1"})

	if len(spy.calls) != 0 {
		t.Errorf("finisher.calls = %d, want 0 (user-2 is still racing)", len(spy.calls))
	}
}

func TestRoomActor_ApplyEvent_ParticipantLeft_EmptyingRoomTriggersFinish(t *testing.T) {
	r := newTestActor()
	spy := &spyFinisher{}
	r.finisher = spy

	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(ParticipantLeft{UserID: "user-1"}) // intentional quit, no disconnect needed first

	if len(spy.calls) != 1 {
		t.Fatalf("finisher.calls = %d, want 1", len(spy.calls))
	}
	if len(spy.calls[0].results) != 1 {
		t.Fatalf("results = %+v, want 1 entry for the quitter", spy.calls[0].results)
	}
	result := spy.calls[0].results[0]
	if result.FinishRank == nil || *result.FinishRank != 1 {
		t.Errorf("FinishRank = %v, want 1 (shared last place, total 1 participant ever joined)", result.FinishRank)
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
	r.applyEvent(ParticipantLeft{UserID: "user-1"})

	if len(spy.calls) != 0 {
		t.Errorf("finisher.calls = %d, want 0 (user-2 is still racing)", len(spy.calls))
	}
}

func TestRoomActor_CheckRaceFinished_QuittersShareLastPlaceRank(t *testing.T) {
	// Mirrors leave-race.md's own example: a 5-player race where nobody
	// finishes and everyone quits mid-race means every one of them is
	// recorded at rank 5 — tied for last, not individually ordered by when
	// they quit.
	r := newTestActor()
	spy := &spyFinisher{}
	r.finisher = spy

	for i := 1; i <= 5; i++ {
		userID := fmt.Sprintf("user-%d", i)
		r.applyEvent(ParticipantJoined{UserID: userID, DisplayName: userID})
	}
	r.applyEvent(ParticipantLeft{UserID: "user-1"})
	r.applyEvent(ParticipantLeft{UserID: "user-2"})
	r.applyEvent(ParticipantDisconnected{UserID: "user-3"})
	r.applyEvent(ParticipantEvicted{UserID: "user-3"})
	r.applyEvent(ParticipantLeft{UserID: "user-4"})
	r.applyEvent(ParticipantLeft{UserID: "user-5"})

	if len(spy.calls) != 1 {
		t.Fatalf("finisher.calls = %d, want 1", len(spy.calls))
	}
	results := spy.calls[0].results
	if len(results) != 5 {
		t.Fatalf("results = %+v, want 5 entries (everyone who ever joined)", results)
	}
	for _, res := range results {
		if res.FinishRank == nil || *res.FinishRank != 5 {
			t.Errorf("user %s: FinishRank = %v, want 5 (shared last place, 5 total participants)", res.UserID, res.FinishRank)
		}
		if res.FinishTimeMs != nil {
			t.Errorf("user %s: FinishTimeMs = %v, want nil (never finished)", res.UserID, res.FinishTimeMs)
		}
	}
}

func TestRoomActor_ApplyEvent_ParticipantJoined_ReconnectDoesNotIncrementTotalParticipants(t *testing.T) {
	r := newTestActor()

	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(ParticipantDisconnected{UserID: "user-1"})
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"}) // reconnect
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"}) // duplicate while connected

	if r.totalParticipants != 1 {
		t.Errorf("totalParticipants = %d, want 1 (only the original join counts)", r.totalParticipants)
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
	r.applyEvent(ParticipantEvicted{UserID: "user-1"})
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
