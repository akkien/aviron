package room

import "testing"

// newTestActor builds a RoomActor with no running goroutine — applyEvent is
// exercised directly, exactly the "pure-ish, no goroutine needed" testing
// approach room-actor-core.md calls for.
func newTestActor() *RoomActor {
	return &RoomActor{
		id:           "race-1",
		participants: make(map[string]*ParticipantState),
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
