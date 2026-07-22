package room

import (
	"context"
	"encoding/json"
	"errors"
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

// noopLeaver satisfies RaceLeaver without touching Postgres, for tests that
// don't care about pending-connections.md's persistence step.
type noopLeaver struct{}

func (noopLeaver) LeaveRace(ctx context.Context, raceID, userID string) error {
	return nil
}

// noopCanceller satisfies RaceCanceller without touching Postgres, for tests
// that don't care about room-lifecycle/cancelled-race-status.md's
// persistence step.
type noopCanceller struct{}

func (noopCanceller) CancelRace(ctx context.Context, raceID string) error {
	return nil
}

// spyCanceller records every CancelRace call, and can be made to fail on
// request, for tests asserting expirePendingRoom's persist-before-broadcast
// ordering and its no-teardown-on-failure guard.
type spyCanceller struct {
	calls []string // raceIDs
	err   error
}

func (s *spyCanceller) CancelRace(ctx context.Context, raceID string) error {
	s.calls = append(s.calls, raceID)
	return s.err
}

// spyLeaver records every LeaveRace call, and can be made to fail on
// request, for tests asserting exactly what a pending leave hands off to be
// persisted (and that a failure is logged, not retried or panicked on).
type spyLeaver struct {
	calls []leaveCall
	err   error
}

type leaveCall struct {
	raceID string
	userID string
}

func (s *spyLeaver) LeaveRace(ctx context.Context, raceID, userID string) error {
	s.calls = append(s.calls, leaveCall{raceID: raceID, userID: userID})
	return s.err
}

// newTestActor builds a RoomActor with no running goroutine — applyEvent is
// exercised directly, exactly the "pure-ish, no goroutine needed" testing
// approach room-actor-core.md calls for. distanceMeters defaults high enough
// that ordinary TelemetryReceived test values (single-digit to low hundreds
// WordsCorrect) never accidentally trigger a finish — tests that actually
// want to exercise finishing set r.distanceMeters explicitly. ctx/cancel are
// real (not nil) so finishRace's r.cancel() call is safe even without Run()
// ever having started. active defaults true: an already-started race is
// what the overwhelming majority of this suite means to simulate (people
// joining, typing, finishing) — a room that never started is its own
// explicit scenario, not the default one.
func newTestActor() *RoomActor {
	ctx, cancel := context.WithCancel(context.Background())
	return &RoomActor{
		id:                   "race-1",
		participants:         make(map[string]*ParticipantState),
		evicted:              make(map[string]struct{}),
		departedParticipants: make(map[string]*ParticipantState),
		distanceMeters:       1_000_000,
		finisher:             noopFinisher{},
		leaver:               noopLeaver{},
		canceller:            noopCanceller{},
		active:               true,
		ctx:                  ctx,
		cancel:               cancel,
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

// TestRoomActor_ApplyEvent_TelemetryReceived_DroppedWhilePending covers
// pending-connections.md's gap: a client connected to a still-pending race
// (already possible since early-spawn.md spawns the actor at creation) must
// not be able to accumulate progress before the race legitimately starts.
func TestRoomActor_ApplyEvent_TelemetryReceived_DroppedWhilePending(t *testing.T) {
	r := newTestActor()
	r.active = false
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})

	r.applyEvent(TelemetryReceived{UserID: "user-1", Seq: 1, WordsCorrect: 5})

	p := r.participants["user-1"]
	if p.WordsCorrect != 0 {
		t.Errorf("WordsCorrect = %d, want 0 (telemetry must be dropped while the race is still pending)", p.WordsCorrect)
	}
	if p.LastSeq != 0 {
		t.Errorf("LastSeq = %d, want 0 (telemetry must be dropped while the race is still pending)", p.LastSeq)
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

func TestRoomActor_ApplyEvent_ParticipantEvicted_RemovesAndEvicts(t *testing.T) {
	r := newTestActor()
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(ParticipantDisconnected{UserID: "user-1"})

	r.applyEvent(ParticipantEvicted{UserID: "user-1"})

	if _, ok := r.participants["user-1"]; ok {
		t.Error("participant still present after ParticipantEvicted, want removed")
	}
	if _, ok := r.evicted["user-1"]; !ok {
		t.Error("user-1 not recorded as evicted after ParticipantEvicted")
	}
}

func TestRoomActor_ApplyEvent_ParticipantEvicted_StaleEventIgnoredAfterReconnect(t *testing.T) {
	r := newTestActor()
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(ParticipantDisconnected{UserID: "user-1"})
	// Reconnect before the (simulated) stale ParticipantEvicted arrives — mirrors
	// the real race between a firing timer and an in-flight reconnect.
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})

	r.applyEvent(ParticipantEvicted{UserID: "user-1"})

	if _, ok := r.participants["user-1"]; !ok {
		t.Error("participant was removed by a stale ParticipantEvicted after reconnecting")
	}
	if _, ok := r.evicted["user-1"]; ok {
		t.Error("user-1 was marked evicted despite having reconnected before the stale event applied")
	}
}

func TestRoomActor_ApplyEvent_ParticipantEvicted_UnknownParticipant(t *testing.T) {
	r := newTestActor()

	// Must not panic for a ParticipantEvicted referencing nobody in the room.
	r.applyEvent(ParticipantEvicted{UserID: "ghost"})

	if len(r.evicted) != 0 {
		t.Errorf("evicted = %v, want empty", r.evicted)
	}
}

func TestRoomActor_ApplyEvent_ParticipantLeft_ActiveRace_RemovesAndEvictsImmediately(t *testing.T) {
	r := newTestActor()
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})

	// Unlike ParticipantEvicted, no ParticipantDisconnected is needed first —
	// an intentional quit is honored while still connected.
	r.applyEvent(ParticipantLeft{UserID: "user-1"})

	if _, ok := r.participants["user-1"]; ok {
		t.Error("participant still present after ParticipantLeft, want removed")
	}
	if _, ok := r.departedParticipants["user-1"]; !ok {
		t.Error("user-1 not tracked in departedParticipants after ParticipantLeft")
	}
	if _, ok := r.evicted["user-1"]; !ok {
		t.Error("user-1 not recorded as evicted after ParticipantLeft")
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

// TestRoomActor_ApplyEvent_ParticipantLeft_Pending_RemovesWithoutDepartedOrEvicted
// covers pending-connections.md's new branch: leaving before the race is
// active goes through r.leaver, not departParticipant — no
// departedParticipants entry, no evicted mark, since someone who backed out
// of a lobby should be free to join again.
func TestRoomActor_ApplyEvent_ParticipantLeft_Pending_RemovesWithoutDepartedOrEvicted(t *testing.T) {
	r := newTestActor()
	r.active = false
	leaver := &spyLeaver{}
	r.leaver = leaver
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})

	r.applyEvent(ParticipantLeft{UserID: "user-1"})

	if _, ok := r.participants["user-1"]; ok {
		t.Error("participant still present after pending ParticipantLeft, want removed")
	}
	if _, ok := r.departedParticipants["user-1"]; ok {
		t.Error("user-1 tracked in departedParticipants after a pending leave, want not tracked (no race result to preserve)")
	}
	if _, ok := r.evicted["user-1"]; ok {
		t.Error("user-1 marked evicted after a pending leave, want not evicted (must be free to rejoin)")
	}
	if len(leaver.calls) != 1 {
		t.Fatalf("leaver.calls = %d, want 1", len(leaver.calls))
	}
	if leaver.calls[0].raceID != r.id || leaver.calls[0].userID != "user-1" {
		t.Errorf("leaver call = %+v, want raceID=%q userID=%q", leaver.calls[0], r.id, "user-1")
	}
}

// TestRoomActor_ApplyEvent_ParticipantLeft_Pending_LeaverFailureIsLoggedNotFatal
// confirms a failed Postgres delete doesn't panic or block the single-writer
// loop — matching finishRace's already-accepted no-retry precedent
// (pending-connections.md). The participant is still removed from the live
// room regardless of the failure, since the connection is already gone by
// the time this runs.
func TestRoomActor_ApplyEvent_ParticipantLeft_Pending_LeaverFailureIsLoggedNotFatal(t *testing.T) {
	r := newTestActor()
	r.active = false
	leaver := &spyLeaver{err: errors.New("db unavailable")}
	r.leaver = leaver
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})

	r.applyEvent(ParticipantLeft{UserID: "user-1"}) // must not panic

	if _, ok := r.participants["user-1"]; ok {
		t.Error("participant still present after pending ParticipantLeft, want removed even though LeaveRace failed")
	}
	if len(leaver.calls) != 1 {
		t.Fatalf("leaver.calls = %d, want 1", len(leaver.calls))
	}
}

// TestRoomActor_ApplyEvent_ParticipantLeft_Pending_LastParticipantCancelsRace
// is a regression test: a pending room's ParticipantLeft branch removed the
// departing participant but never called checkRaceFinished afterward (unlike
// departParticipant, used by the active-race leave path just above), so the
// last player leaving a pending lobby left the race sitting as "pending"
// forever instead of being cancelled — nobody would see it cancel until
// PendingTimeoutDuration eventually elapsed on its own.
func TestRoomActor_ApplyEvent_ParticipantLeft_Pending_LastParticipantCancelsRace(t *testing.T) {
	r := newTestActor()
	r.active = false
	r.broadcast = make(chan []byte, 4)
	r.leaver = &spyLeaver{}
	canceller := &spyCanceller{}
	r.canceller = canceller
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})

	r.applyEvent(ParticipantLeft{UserID: "user-1"})

	if len(canceller.calls) != 1 {
		t.Fatalf("canceller.calls = %d, want 1 (last participant leaving a pending room must cancel it)", len(canceller.calls))
	}
	if canceller.calls[0] != r.id {
		t.Errorf("canceller called with raceID = %q, want %q", canceller.calls[0], r.id)
	}
	if !r.finished {
		t.Error("r.finished = false after the last pending participant left, want true")
	}
}

// TestRoomActor_ApplyEvent_ParticipantLeft_Pending_OthersRemaining_DoesNotCancel
// confirms the fix above didn't overreach: leaving a pending room that still
// has other participants must not cancel it.
func TestRoomActor_ApplyEvent_ParticipantLeft_Pending_OthersRemaining_DoesNotCancel(t *testing.T) {
	r := newTestActor()
	r.active = false
	r.broadcast = make(chan []byte, 4)
	r.leaver = &spyLeaver{}
	canceller := &spyCanceller{}
	r.canceller = canceller
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(ParticipantJoined{UserID: "user-2", DisplayName: "Bob"})

	r.applyEvent(ParticipantLeft{UserID: "user-1"})

	if len(canceller.calls) != 0 {
		t.Errorf("canceller.calls = %d, want 0 (user-2 is still in the pending room)", len(canceller.calls))
	}
	if r.finished {
		t.Error("r.finished = true, want false (room still has a participant)")
	}
	if _, ok := r.participants["user-2"]; !ok {
		t.Error("user-2 missing from participants after user-1 left, want still present")
	}
}

func TestRoomActor_ApplyEvent_EvictionQuery(t *testing.T) {
	r := newTestActor()
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	r.applyEvent(ParticipantDisconnected{UserID: "user-1"})
	r.applyEvent(ParticipantEvicted{UserID: "user-1"})

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

// TestRoomActor_ApplyEvent_Activated_SetsActiveAndBroadcastsRaceStarted covers
// websocket/race-started-broadcast.md: MarkActive's activated event must both
// flip r.active (already covered by pending-connections.md's own gating
// tests) and broadcast race_started carrying the race's prompt text, so
// already-connected pending clients learn the race started without a
// separate GET /races/{id}/text round-trip.
func TestRoomActor_ApplyEvent_Activated_SetsActiveAndBroadcastsRaceStarted(t *testing.T) {
	r := newTestActor()
	r.active = false
	r.broadcast = make(chan []byte, 4)

	r.applyEvent(activated{PromptText: "the quick brown fox"})

	if !r.active {
		t.Error("r.active = false after activated, want true")
	}

	select {
	case body := <-r.broadcast:
		var msg RaceStartedMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			t.Fatalf("unmarshal broadcast: %v", err)
		}
		if msg.Type != "race_started" {
			t.Errorf("Type = %q, want %q", msg.Type, "race_started")
		}
		if msg.PromptText != "the quick brown fox" {
			t.Errorf("PromptText = %q, want %q", msg.PromptText, "the quick brown fox")
		}
	default:
		t.Fatal("no race_started message broadcast")
	}
}

// TestRoomActor_ApplyEvent_PendingExpired_WhilePending_BroadcastsAndTearsDown
// covers room-lifecycle/pending-expiry.md's core case: a room that's still
// pending when its timer fires must broadcast race_expired and tear down
// with zero Postgres writes, even with participants still attached — the
// gap that didn't exist before this feature (checkRaceFinished's own
// !r.active teardown only ever fired for an empty room).
func TestRoomActor_ApplyEvent_PendingExpired_WhilePending_BroadcastsAndTearsDown(t *testing.T) {
	r := newTestActor()
	r.active = false
	r.broadcast = make(chan []byte, 4)
	spy := &spyFinisher{}
	r.finisher = spy
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	<-r.broadcast // drain the immediate join snapshot

	r.applyEvent(pendingExpired{})

	if !r.finished {
		t.Error("r.finished = false after pendingExpired, want true")
	}
	select {
	case <-r.ctx.Done():
	default:
		t.Error("r.ctx not cancelled after pendingExpired")
	}
	if len(spy.calls) != 0 {
		t.Errorf("finisher.calls = %d, want 0 — a room that never went active has no real race to persist", len(spy.calls))
	}
	select {
	case body := <-r.broadcast:
		var msg RaceExpiredMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			t.Fatalf("unmarshal broadcast: %v", err)
		}
		if msg.Type != "race_expired" {
			t.Errorf("Type = %q, want %q", msg.Type, "race_expired")
		}
	default:
		t.Fatal("no race_expired message broadcast")
	}
}

// TestRoomActor_ApplyEvent_PendingExpired_WhileActive_NoOp is the spec's own
// explicitly-required race-vs-real-start test: the timer firing concurrently
// with a real start must never tear down a race that's actually running.
func TestRoomActor_ApplyEvent_PendingExpired_WhileActive_NoOp(t *testing.T) {
	r := newTestActor() // active: true by default
	r.broadcast = make(chan []byte, 4)

	r.applyEvent(pendingExpired{})

	if r.finished {
		t.Error("r.finished = true after pendingExpired while active, want false")
	}
	select {
	case <-r.ctx.Done():
		t.Error("r.ctx was cancelled after pendingExpired while active, want untouched")
	default:
	}
	select {
	case body := <-r.broadcast:
		t.Errorf("unexpected broadcast while active: %s", body)
	default:
	}
}

// TestRoomActor_ApplyEvent_PendingExpired_AlreadyFinished_NoOp confirms the
// timer firing after the room already tore down for some other reason
// (e.g. everyone quit) doesn't double-broadcast or re-cancel.
func TestRoomActor_ApplyEvent_PendingExpired_AlreadyFinished_NoOp(t *testing.T) {
	r := newTestActor()
	r.active = false
	r.finished = true
	r.broadcast = make(chan []byte, 4)

	r.applyEvent(pendingExpired{})

	select {
	case body := <-r.broadcast:
		t.Errorf("unexpected broadcast for an already-finished room: %s", body)
	default:
	}
}

// TestRoomActor_ExpirePendingRoom_CallsCancellerWithRaceID covers
// room-lifecycle/cancelled-race-status.md: expirePendingRoom must persist
// the cancellation, identified by this room's own raceID.
func TestRoomActor_ExpirePendingRoom_CallsCancellerWithRaceID(t *testing.T) {
	r := newTestActor()
	r.active = false
	r.broadcast = make(chan []byte, 4)
	canceller := &spyCanceller{}
	r.canceller = canceller

	r.applyEvent(pendingExpired{})

	if len(canceller.calls) != 1 {
		t.Fatalf("canceller.calls = %d, want 1", len(canceller.calls))
	}
	if canceller.calls[0] != r.id {
		t.Errorf("canceller called with raceID = %q, want %q", canceller.calls[0], r.id)
	}
	if !r.finished {
		t.Error("r.finished = false after a successful cancel, want true")
	}
}

// TestRoomActor_ExpirePendingRoom_CancellerFailure_LeavesRoomRunning mirrors
// finishRace's own already-accepted no-retry precedent: a failed persist
// must not broadcast race_expired, must not set r.finished, and must not
// cancel r.ctx — the room stays running rather than silently vanishing on a
// Postgres hiccup.
func TestRoomActor_ExpirePendingRoom_CancellerFailure_LeavesRoomRunning(t *testing.T) {
	r := newTestActor()
	r.active = false
	r.broadcast = make(chan []byte, 4)
	canceller := &spyCanceller{err: errors.New("db unavailable")}
	r.canceller = canceller

	r.applyEvent(pendingExpired{})

	if len(canceller.calls) != 1 {
		t.Fatalf("canceller.calls = %d, want 1", len(canceller.calls))
	}
	if r.finished {
		t.Error("r.finished = true after a failed cancel, want false")
	}
	select {
	case <-r.ctx.Done():
		t.Error("r.ctx was cancelled after a failed cancel, want untouched")
	default:
	}
	select {
	case body := <-r.broadcast:
		t.Errorf("unexpected race_expired broadcast after a failed cancel: %s", body)
	default:
	}
}
