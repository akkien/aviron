package room

import (
	"context"
	"testing"
)

// noopFinisher satisfies RaceFinisher without touching Postgres, for tests
// that don't care about race-completion/finish-race.md's persistence step.
type noopFinisher struct{}

func (noopFinisher) FinishRace(ctx context.Context, raceID string, distanceMeters int, results []ParticipantResult) error {
	return nil
}

// spyFinisher records every FinishRace call, for tests that assert exactly
// what the room actor handed off to be persisted.
type spyFinisher struct {
	calls []finishCall
}

type finishCall struct {
	raceID         string
	distanceMeters int
	results        []ParticipantResult
}

func (s *spyFinisher) FinishRace(ctx context.Context, raceID string, distanceMeters int, results []ParticipantResult) error {
	s.calls = append(s.calls, finishCall{raceID: raceID, distanceMeters: distanceMeters, results: results})
	return nil
}

// newTestActor builds a RoomActor with no running goroutine — applyEvent is
// exercised directly, exactly the "pure-ish, no goroutine needed" testing
// approach room-actor-core.md calls for. distanceMeters defaults high enough
// that ordinary TelemetryReceived test values (single-digit to low hundreds
// WordsCorrect) never accidentally trigger a finish — tests that actually
// want to exercise finishing set r.distanceMeters explicitly. ctx/cancel are
// real (not nil) so finishRace's r.cancel() call is safe even without Run()
// ever having started.
func newTestActor() *RoomActor {
	ctx, cancel := context.WithCancel(context.Background())
	return &RoomActor{
		id:             "race-1",
		participants:   make(map[string]*ParticipantState),
		evicted:        make(map[string]struct{}),
		distanceMeters: 1_000_000,
		finisher:       noopFinisher{},
		ctx:            ctx,
		cancel:         cancel,
	}
}

func TestRoomActor_ApplyEvent_ParticipantJoined(t *testing.T) {
	r := newTestActor()

	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})

	p, ok := r.participants["user-1"]
	if !ok {
		t.Fatalf("participants[user-1] missing after join")
	}
	if p.DisplayName != "Alice" {
		t.Errorf("DisplayName = %q, want %q", p.DisplayName, "Alice")
	}
	if p.WordsCorrect != 0 {
		t.Errorf("WordsCorrect = %d, want 0", p.WordsCorrect)
	}
	if p.ConnectedAt.IsZero() {
		t.Error("ConnectedAt is zero, want set")
	}
}

func TestRoomActor_ApplyEvent_TelemetryReceived(t *testing.T) {
	r := newTestActor()
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})

	r.applyEvent(TelemetryReceived{UserID: "user-1", Seq: 1, WordsCorrect: 5})

	p := r.participants["user-1"]
	if p.WordsCorrect != 5 {
		t.Errorf("WordsCorrect = %d, want 5", p.WordsCorrect)
	}
	if p.LastSeq != 1 {
		t.Errorf("LastSeq = %d, want 1", p.LastSeq)
	}
}

func TestRoomActor_ApplyEvent_TelemetryReceived_StaleSeqDropped(t *testing.T) {
	r := newTestActor()
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(TelemetryReceived{UserID: "user-1", Seq: 5, WordsCorrect: 10})

	// A duplicate/out-of-order message with a lower or equal seq must not
	// move WordsCorrect backwards.
	r.applyEvent(TelemetryReceived{UserID: "user-1", Seq: 3, WordsCorrect: 999})
	r.applyEvent(TelemetryReceived{UserID: "user-1", Seq: 5, WordsCorrect: 999})

	p := r.participants["user-1"]
	if p.WordsCorrect != 10 {
		t.Errorf("WordsCorrect = %d, want 10 (stale/duplicate updates should be dropped)", p.WordsCorrect)
	}
	if p.LastSeq != 5 {
		t.Errorf("LastSeq = %d, want 5", p.LastSeq)
	}
}

func TestRoomActor_ApplyEvent_TelemetryReceived_UnknownParticipantDropped(t *testing.T) {
	r := newTestActor()

	// No ParticipantJoined for user-1 — must not panic or create a
	// half-initialized entry.
	r.applyEvent(TelemetryReceived{UserID: "user-1", Seq: 1, WordsCorrect: 5})

	if _, ok := r.participants["user-1"]; ok {
		t.Error("participants[user-1] should not exist for telemetry from an unknown participant")
	}
}

func TestRoomActor_ApplyEvent_ParticipantDisconnected(t *testing.T) {
	r := newTestActor()
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})

	r.applyEvent(ParticipantDisconnected{UserID: "user-1"})

	p := r.participants["user-1"]
	if p.DisconnectedAt == nil {
		t.Fatal("DisconnectedAt is nil, want set")
	}
	// The participant stays in the room (grace period is a later feature's
	// concern) — it must not be removed here.
	if _, ok := r.participants["user-1"]; !ok {
		t.Error("participant was removed on disconnect; should be kept for the grace period")
	}
}

func TestRoomActor_ApplyEvent_ParticipantDisconnected_UnknownParticipant(t *testing.T) {
	r := newTestActor()

	// Must not panic for a disconnect event referencing nobody in the room.
	r.applyEvent(ParticipantDisconnected{UserID: "ghost"})

	if len(r.participants) != 0 {
		t.Errorf("participants = %v, want empty", r.participants)
	}
}

func TestRoomActor_ApplyEvent_ParticipantDisconnected_StartsGraceTimerAndCountsIt(t *testing.T) {
	r := newTestActor()
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})

	r.applyEvent(ParticipantDisconnected{UserID: "user-1"})

	p := r.participants["user-1"]
	if p.graceTimer == nil {
		t.Error("graceTimer is nil, want a pending grace-period timer")
	}
	if p.DisconnectedCount != 1 {
		t.Errorf("DisconnectedCount = %d, want 1", p.DisconnectedCount)
	}

	// A second disconnect (e.g. after reconnecting elsewhere) counts again
	// and replaces the pending timer rather than leaving two active.
	r.applyEvent(ParticipantDisconnected{UserID: "user-1"})
	if p.DisconnectedCount != 2 {
		t.Errorf("DisconnectedCount = %d, want 2 after a second disconnect", p.DisconnectedCount)
	}
}

func TestRoomActor_ApplyEvent_ParticipantJoined_ReconnectPreservesProgress(t *testing.T) {
	r := newTestActor()
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(TelemetryReceived{UserID: "user-1", Seq: 5, WordsCorrect: 42})
	r.applyEvent(ParticipantDisconnected{UserID: "user-1"})

	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})

	if len(r.participants) != 1 {
		t.Fatalf("participants = %d, want 1 (no duplicate participant on reconnect)", len(r.participants))
	}
	p := r.participants["user-1"]
	if p.DisconnectedAt != nil {
		t.Error("DisconnectedAt should be cleared after reconnect")
	}
	if p.graceTimer != nil {
		t.Error("graceTimer should be cleared (stopped) after reconnect")
	}
	if p.WordsCorrect != 42 {
		t.Errorf("WordsCorrect = %d, want 42 (progress preserved across reconnect)", p.WordsCorrect)
	}
	if p.LastSeq != 5 {
		t.Errorf("LastSeq = %d, want 5 (progress preserved across reconnect)", p.LastSeq)
	}
}

func TestRoomActor_ApplyEvent_ParticipantJoined_DuplicateWhileConnected_PreservesProgress(t *testing.T) {
	r := newTestActor()
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(TelemetryReceived{UserID: "user-1", Seq: 5, WordsCorrect: 42})

	// A duplicate join_race while still connected (e.g. a client retry, two
	// tabs) must not reset progress like a fresh join would.
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})

	if len(r.participants) != 1 {
		t.Fatalf("participants = %d, want 1 (no duplicate participant)", len(r.participants))
	}
	p := r.participants["user-1"]
	if p.WordsCorrect != 42 {
		t.Errorf("WordsCorrect = %d, want 42 (progress preserved across duplicate join)", p.WordsCorrect)
	}
	if p.LastSeq != 5 {
		t.Errorf("LastSeq = %d, want 5 (progress preserved across duplicate join)", p.LastSeq)
	}
	if p.DisconnectedAt != nil {
		t.Error("DisconnectedAt should stay nil — this participant was never disconnected")
	}
}

func TestRoomActor_ApplyEvent_ParticipantLeft_RemovesAndEvicts(t *testing.T) {
	r := newTestActor()
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(ParticipantDisconnected{UserID: "user-1"})

	r.applyEvent(ParticipantLeft{UserID: "user-1"})

	if _, ok := r.participants["user-1"]; ok {
		t.Error("participant still present after ParticipantLeft, want removed")
	}
	if _, ok := r.evicted["user-1"]; !ok {
		t.Error("user-1 not recorded as evicted after ParticipantLeft")
	}
}

func TestRoomActor_ApplyEvent_ParticipantLeft_StaleEventIgnoredAfterReconnect(t *testing.T) {
	r := newTestActor()
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(ParticipantDisconnected{UserID: "user-1"})
	// Reconnect before the (simulated) stale ParticipantLeft arrives — mirrors
	// the real race between a firing timer and an in-flight reconnect.
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})

	r.applyEvent(ParticipantLeft{UserID: "user-1"})

	if _, ok := r.participants["user-1"]; !ok {
		t.Error("participant was removed by a stale ParticipantLeft after reconnecting")
	}
	if _, ok := r.evicted["user-1"]; ok {
		t.Error("user-1 was marked evicted despite having reconnected before the stale event applied")
	}
}

func TestRoomActor_ApplyEvent_ParticipantLeft_UnknownParticipant(t *testing.T) {
	r := newTestActor()

	// Must not panic for a ParticipantLeft referencing nobody in the room.
	r.applyEvent(ParticipantLeft{UserID: "ghost"})

	if len(r.evicted) != 0 {
		t.Errorf("evicted = %v, want empty", r.evicted)
	}
}

func TestRoomActor_ApplyEvent_EvictionQuery(t *testing.T) {
	r := newTestActor()
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(ParticipantDisconnected{UserID: "user-1"})
	r.applyEvent(ParticipantLeft{UserID: "user-1"})

	evictedReply := make(chan bool, 1)
	r.applyEvent(evictionQuery{UserID: "user-1", Reply: evictedReply})
	if evicted := <-evictedReply; !evicted {
		t.Error("evictionQuery reported user-1 as not evicted, want evicted")
	}

	notEvictedReply := make(chan bool, 1)
	r.applyEvent(evictionQuery{UserID: "user-2", Reply: notEvictedReply})
	if evicted := <-notEvictedReply; evicted {
		t.Error("evictionQuery reported user-2 as evicted, want not evicted")
	}
}
