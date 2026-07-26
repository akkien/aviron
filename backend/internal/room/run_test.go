package room

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

// withShortGracePeriod temporarily shortens gracePeriodDuration so tests
// don't have to wait a real 30s, restoring it afterward.
func withShortGracePeriod(t *testing.T, d time.Duration) {
	t.Helper()
	original := gracePeriodDuration
	gracePeriodDuration = d
	t.Cleanup(func() { gracePeriodDuration = original })
}

// withShortNoShowTimeout temporarily shortens noShowTimeoutDuration so tests
// don't have to wait a real 30s, restoring it afterward.
func withShortNoShowTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	original := noShowTimeoutDuration
	noShowTimeoutDuration = d
	t.Cleanup(func() { noShowTimeoutDuration = original })
}

// withShortPendingTimeout temporarily shortens PendingTimeoutDuration so
// tests don't have to wait a real 5 minutes, restoring it afterward.
// Exported (unlike gracePeriodDuration/noShowTimeoutDuration) since
// internal/race's handler and internal/ws's own regression test both need
// to reach it from a different package — this helper just reuses that same
// exported identifier for consistency with the other two.
func withShortPendingTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	original := PendingTimeoutDuration
	PendingTimeoutDuration = d
	t.Cleanup(func() { PendingTimeoutDuration = original })
}

// awaitMessageType drains broadcast until it sees a message with the given
// "type" field (skipping any race_state ticks that land first — the room
// actor broadcasts on a 250ms ticker independently of a test's own timers)
// or fails the test after timeout.
func awaitMessageType(t *testing.T, broadcast <-chan []byte, wantType string, timeout time.Duration) map[string]any {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case body := <-broadcast:
			var envelope map[string]any
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatalf("unmarshal message: %v", err)
			}
			if envelope["type"] == wantType {
				return envelope
			}
		case <-deadline:
			t.Fatalf("never received a %q message", wantType)
			return nil
		}
	}
}

func TestRoomActor_Run_NoShowTimeout_NeverActiveEmptyRoom_TearsDownWithoutFinishing(t *testing.T) {
	withShortNoShowTimeout(t, 50*time.Millisecond)

	broadcast := make(chan []byte, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	spy := &spyFinisher{}

	// Never MarkActive()'d: nobody ever joined and the race never started
	// (pending-connections.md) — there's no real race to persist, so the
	// room tears down without ever calling the finisher.
	r := NewRoomActor(ctx, "race-1", 5, broadcast, spy, noopLeaver{}, noopCanceller{}, NoopPublisher{}, testLogger, testTickObserver)
	go r.Run()

	select {
	case <-r.Context().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("room was not torn down after the no-show timeout fired")
	}

	if len(spy.calls) != 0 {
		t.Fatalf("finisher.calls = %d, want 0 — a room that never went active has no real race to persist", len(spy.calls))
	}
}

func TestRoomActor_Run_NoShowTimeout_NoopIfSomeoneJoined(t *testing.T) {
	withShortNoShowTimeout(t, 50*time.Millisecond)

	broadcast := make(chan []byte, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	spy := &spyFinisher{}

	r := NewRoomActor(ctx, "race-1", 5, broadcast, spy, noopLeaver{}, noopCanceller{}, NoopPublisher{}, testLogger, testTickObserver)
	go r.Run()

	r.Send(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	<-broadcast // drain the immediate join snapshot

	// Let the (shortened) no-show timeout fire and confirm it was a no-op.
	time.Sleep(150 * time.Millisecond)

	if len(spy.calls) != 0 {
		t.Errorf("finisher.calls = %d, want 0 (user-1 joined before the timeout)", len(spy.calls))
	}
	select {
	case <-r.Context().Done():
		t.Error("room was torn down despite an active, unfinished participant")
	default:
	}
}

// TestRoomActor_Run_PendingTimeoutExpiry_TearsDownRoomWithParticipants proves
// room-lifecycle/pending-expiry.md's actual gap is closed: unlike
// noShowTimeout (which only ever tears down an empty room), a still-pending
// room with a participant genuinely attached must tear down once
// PendingTimeoutDuration elapses, broadcasting race_expired first.
func TestRoomActor_Run_PendingTimeoutExpiry_TearsDownRoomWithParticipants(t *testing.T) {
	withShortPendingTimeout(t, 50*time.Millisecond)

	broadcast := make(chan []byte, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	spy := &spyFinisher{}

	r := NewRoomActor(ctx, "race-1", 5, broadcast, spy, noopLeaver{}, noopCanceller{}, NoopPublisher{}, testLogger, testTickObserver)
	go r.Run()

	r.Send(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	<-broadcast // drain the immediate join snapshot

	msg := awaitMessageType(t, broadcast, "race_expired", 2*time.Second)
	if msg["type"] != "race_expired" {
		t.Errorf("Type = %v, want %q", msg["type"], "race_expired")
	}

	select {
	case <-r.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("room was not torn down after the pending timeout fired")
	}

	if len(spy.calls) != 0 {
		t.Fatalf("finisher.calls = %d, want 0 — a room that never went active has no real race to persist", len(spy.calls))
	}
}

func TestRoomActor_Run_GracePeriodExpiry_RemovesAndEvictsParticipant(t *testing.T) {
	withShortGracePeriod(t, 50*time.Millisecond)

	broadcast := make(chan []byte, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := NewRoomActor(ctx, "race-1", 5, broadcast, noopFinisher{}, noopLeaver{}, noopCanceller{}, NoopPublisher{}, testLogger, testTickObserver)
	go r.Run()

	// A second, still-racing participant keeps the room alive once user-1 is
	// evicted — race-completion/finish-race.md means an empty room finishes
	// and self-cancels, at which point IsEvicted always answers false ("room's
	// gone, nothing left to be evicted from"). With user-2 still unfinished,
	// this test stays isolated to grace-period eviction, not that behavior.
	r.Send(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	<-broadcast // drain the immediate join snapshot
	r.Send(ParticipantJoined{UserID: "user-2", DisplayName: "Bob"})
	<-broadcast

	r.Send(ParticipantDisconnected{UserID: "user-1"})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.IsEvicted("user-1") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("user-1 was not evicted after the grace period expired")
}

func TestRoomActor_Run_ReconnectWithinGracePeriod_CancelsTimer(t *testing.T) {
	withShortGracePeriod(t, 100*time.Millisecond)

	broadcast := make(chan []byte, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := NewRoomActor(ctx, "race-1", 5, broadcast, noopFinisher{}, noopLeaver{}, noopCanceller{}, NoopPublisher{}, testLogger, testTickObserver)
	go r.Run()

	r.Send(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	<-broadcast

	r.Send(ParticipantDisconnected{UserID: "user-1"})

	// Reconnect well before the (shortened) grace period would expire.
	time.Sleep(20 * time.Millisecond)
	r.Send(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})

	// Give the original timer time to have fired if it hadn't been cancelled.
	time.Sleep(150 * time.Millisecond)

	if r.IsEvicted("user-1") {
		t.Error("user-1 was evicted despite reconnecting within the grace period")
	}
}

func TestRoomActor_IsEvicted_UnknownUser(t *testing.T) {
	broadcast := make(chan []byte, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := NewRoomActor(ctx, "race-1", 5, broadcast, noopFinisher{}, noopLeaver{}, noopCanceller{}, NoopPublisher{}, testLogger, testTickObserver)
	go r.Run()

	if r.IsEvicted("never-joined") {
		t.Error("IsEvicted(never-joined) = true, want false")
	}
}

func TestRoomActor_IsEvicted_DoesNotBlockAfterContextCancelled(t *testing.T) {
	broadcast := make(chan []byte, 4)
	ctx, cancel := context.WithCancel(context.Background())

	r := NewRoomActor(ctx, "race-1", 5, broadcast, noopFinisher{}, noopLeaver{}, noopCanceller{}, NoopPublisher{}, testLogger, testTickObserver)
	go r.Run()
	cancel()
	time.Sleep(50 * time.Millisecond) // let Run() actually exit first

	done := make(chan struct{})
	go func() {
		r.IsEvicted("user-1")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("IsEvicted() blocked after context cancellation — would leak the calling goroutine")
	}
}

func TestRoomActor_Send_DeliversToInbox(t *testing.T) {
	broadcast := make(chan []byte, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := NewRoomActor(ctx, "race-1", 3, broadcast, noopFinisher{}, noopLeaver{}, noopCanceller{}, NoopPublisher{}, testLogger, testTickObserver)
	go r.Run()

	r.Send(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})

	select {
	case <-broadcast:
	case <-time.After(time.Second):
		t.Fatal("no broadcast received within 1s after Send — event was not applied")
	}
}

func TestRoomActor_Send_DoesNotBlockAfterContextCancelled(t *testing.T) {
	broadcast := make(chan []byte, 4)
	ctx, cancel := context.WithCancel(context.Background())

	r := NewRoomActor(ctx, "race-1", 3, broadcast, noopFinisher{}, noopLeaver{}, noopCanceller{}, NoopPublisher{}, testLogger, testTickObserver)
	go r.Run()
	cancel()
	time.Sleep(50 * time.Millisecond) // let Run() actually exit before Send

	done := make(chan struct{})
	go func() {
		r.Send(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Send() blocked after context cancellation — would leak the calling goroutine")
	}
}

func TestRoomActor_Context_DoneAfterCancel(t *testing.T) {
	broadcast := make(chan []byte, 4)
	ctx, cancel := context.WithCancel(context.Background())

	r := NewRoomActor(ctx, "race-1", 3, broadcast, noopFinisher{}, noopLeaver{}, noopCanceller{}, NoopPublisher{}, testLogger, testTickObserver)
	cancel()

	select {
	case <-r.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("Context().Done() not closed after cancel")
	}
}

func TestRoomActor_Run_BroadcastsOnTick(t *testing.T) {
	broadcast := make(chan []byte, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := NewRoomActor(ctx, "race-1", 3, broadcast, noopFinisher{}, noopLeaver{}, noopCanceller{}, NoopPublisher{}, testLogger, testTickObserver)
	r.applyEvent(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})

	go r.Run()

	select {
	case body := <-broadcast:
		var msg RaceStateMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			t.Fatalf("unmarshal broadcast: %v", err)
		}
		if msg.Type != "race_state" {
			t.Errorf("Type = %q, want %q", msg.Type, "race_state")
		}
		if len(msg.Participants) != 1 {
			t.Fatalf("Participants = %d, want 1", len(msg.Participants))
		}
		if msg.Participants[0].UserID != "user-1" {
			t.Errorf("UserID = %q, want %q", msg.Participants[0].UserID, "user-1")
		}
		if msg.Participants[0].Rank != 1 {
			t.Errorf("Rank = %d, want 1", msg.Participants[0].Rank)
		}
	case <-time.After(time.Second):
		t.Fatal("no broadcast received within 1s (expected a tick every 250ms)")
	}
}

// spyTickObserver records every ObserveTick call onto a channel rather than
// a plain slice — Run()'s ticker branch calls it from the actor's own
// goroutine, so a channel is what lets the test goroutine read the result
// without racing that write (go test -race).
type spyTickObserver struct {
	calls chan time.Duration
}

func newSpyTickObserver() *spyTickObserver {
	return &spyTickObserver{calls: make(chan time.Duration, 8)}
}

func (s *spyTickObserver) ObserveTick(d time.Duration) {
	s.calls <- d
}

func TestRoomActor_Run_ObservesTickLatency(t *testing.T) {
	broadcast := make(chan []byte, 4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	spy := newSpyTickObserver()
	r := NewRoomActor(ctx, "race-1", 3, broadcast, noopFinisher{}, noopLeaver{}, noopCanceller{}, NoopPublisher{}, testLogger, spy)

	go r.Run()

	select {
	case d := <-spy.calls:
		if d < 0 {
			t.Errorf("ObserveTick duration = %v, want >= 0", d)
		}
	case <-time.After(time.Second):
		t.Fatal("ObserveTick was never called within 1s (expected a tick every 250ms)")
	}
}

func TestRoomActor_Run_StopsOnContextCancel(t *testing.T) {
	broadcast := make(chan []byte, 4)
	ctx, cancel := context.WithCancel(context.Background())

	r := NewRoomActor(ctx, "race-1", 3, broadcast, noopFinisher{}, noopLeaver{}, noopCanceller{}, NoopPublisher{}, testLogger, testTickObserver)

	done := make(chan struct{})
	go func() {
		r.Run()
		close(done)
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run() did not return after context cancellation — goroutine leak")
	}
}

func TestRoomActor_Run_ConcurrentInboxSenders(t *testing.T) {
	broadcast := make(chan []byte, 256)
	ctx, cancel := context.WithCancel(context.Background())

	r := NewRoomActor(ctx, "race-1", 100, broadcast, noopFinisher{}, noopLeaver{}, noopCanceller{}, NoopPublisher{}, testLogger, testTickObserver)
	r.active = true // telemetry is dropped while pending (pending-connections.md)

	const numParticipants = 5
	const eventsPerParticipant = 50

	// Seed participants before starting Run() — no goroutine is running yet,
	// so calling applyEvent directly here is safe.
	for i := range numParticipants {
		r.applyEvent(ParticipantJoined{
			UserID:      fmt.Sprintf("user-%d", i),
			DisplayName: fmt.Sprintf("User %d", i),
		})
	}

	done := make(chan struct{})
	go func() {
		r.Run()
		close(done)
	}()

	// Drain broadcasts concurrently so broadcastSnapshot's non-blocking send
	// never has a reason to drop a tick while the inbox is still filling.
	stopDraining := make(chan struct{})
	go func() {
		for {
			select {
			case <-broadcast:
			case <-stopDraining:
				return
			}
		}
	}()

	var wg sync.WaitGroup
	for i := range numParticipants {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			userID := fmt.Sprintf("user-%d", idx)
			for seq := 1; seq <= eventsPerParticipant; seq++ {
				r.inbox <- TelemetryReceived{UserID: userID, Seq: seq, WordsCorrect: seq}
			}
		}(i)
	}
	wg.Wait()

	// Give the single-writer goroutine time to drain the inbox before we
	// stop it — cancelling immediately could race a still-buffered event
	// against ctx.Done() inside Run()'s select.
	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after context cancellation")
	}
	close(stopDraining)

	// Run() has returned, so the actor goroutine is provably done touching
	// participants — reading it directly here is safe now.
	for i := range numParticipants {
		userID := fmt.Sprintf("user-%d", i)
		p, ok := r.participants[userID]
		if !ok {
			t.Fatalf("participants[%s] missing", userID)
		}
		if p.WordsCorrect != eventsPerParticipant {
			t.Errorf("%s: WordsCorrect = %d, want %d", userID, p.WordsCorrect, eventsPerParticipant)
		}
		if p.LastSeq != eventsPerParticipant {
			t.Errorf("%s: LastSeq = %d, want %d", userID, p.LastSeq, eventsPerParticipant)
		}
	}
}
