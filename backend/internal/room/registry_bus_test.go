package room

import (
	"context"
	"sync"
	"testing"
	"time"
)

// busLogEntry records one spyBus.PublishOut/PublishRoomClosed call, in the
// order it happened.
type busLogEntry struct {
	kind    string // "publish_out" or "publish_room_closed"
	payload string
}

// spyBus is a RoomBus test double: SubscribeIn always hands back the same
// test-controlled channel (in), letting a test feed RoomEvents directly as
// if they'd arrived over the bus, while PublishOut/PublishRoomClosed
// record every call in order — proving Spawn's feedInbox/drainBroadcast
// goroutines actually wire the bus in, not just accept the parameter.
type spyBus struct {
	in  chan RoomEvent
	mu  sync.Mutex
	log []busLogEntry
}

func newSpyBus() *spyBus {
	return &spyBus{in: make(chan RoomEvent)}
}

func (b *spyBus) SubscribeIn(ctx context.Context, raceID string) (<-chan RoomEvent, func(), error) {
	return b.in, func() {}, nil
}

func (b *spyBus) PublishOut(ctx context.Context, raceID string, payload []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.log = append(b.log, busLogEntry{kind: "publish_out", payload: string(payload)})
	return nil
}

func (b *spyBus) PublishRoomClosed(ctx context.Context, raceID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.log = append(b.log, busLogEntry{kind: "publish_room_closed"})
	return nil
}

func (b *spyBus) snapshot() []busLogEntry {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]busLogEntry(nil), b.log...)
}

// TestRegistry_Spawn_FeedsInboxFromBus proves feedInbox actually wires a
// RoomEvent arriving over the bus into the room actor's single-writer
// inbox (room-service-adapter.md), not just accepts the RoomBus parameter.
// Asserting via spyBus's own PublishOut log — not by reading
// actor.Broadcast() directly — since drainBroadcast is also permanently
// draining that channel; a second reader in the test would race it.
func TestRegistry_Spawn_FeedsInboxFromBus(t *testing.T) {
	bus := newSpyBus()
	reg := NewRegistry(testLogger, testTickObserver, NoopLocator{}, NoopPublisher{}, bus, NoopEvictionRecorder{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	reg.Spawn(ctx, "race-1", 5, noopFinisher{}, noopLeaver{}, noopCanceller{})

	select {
	case bus.in <- ParticipantJoined{UserID: "user-1", DisplayName: "Alice"}:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out sending ParticipantJoined through the bus")
	}

	// ParticipantJoined broadcasts a snapshot immediately (room.go's
	// applyEvent) — a PublishOut call proves the event reached applyEvent
	// via feedInbox and came back out through drainBroadcast.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(bus.snapshot()) > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for a PublishOut call after ParticipantJoined arrived via the bus")
}

// TestRegistry_Spawn_DrainsBroadcastBeforePublishingRoomClosed is the
// regression test for a real gap found while implementing this feature:
// actor.Broadcast()'s channel is never closed by RoomActor (its shutdown
// signal is always ctx), so a naive `for range actor.Broadcast()` would
// hang forever and never publish room_closed. distanceMeters=1 makes the
// sole participant's first telemetry both finish them and finish the whole
// race (lifecycle.go) — finishRace sends its final broadcast(s) and cancels
// ctx right after, without blocking, the exact broadcast-vs-done race
// internal/wsgateway/hub.go's own hub.run has to guard against too.
func TestRegistry_Spawn_DrainsBroadcastBeforePublishingRoomClosed(t *testing.T) {
	bus := newSpyBus()
	reg := NewRegistry(testLogger, testTickObserver, NoopLocator{}, NoopPublisher{}, bus, NoopEvictionRecorder{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	actor := reg.Spawn(ctx, "race-1", 1, noopFinisher{}, noopLeaver{}, noopCanceller{})
	// A newly spawned room starts pending (active: false) until the race is
	// started — TelemetryReceived is silently dropped until then (room.go),
	// so this race would otherwise never finish.
	actor.MarkActive("hello world")

	send := func(ev RoomEvent) {
		select {
		case bus.in <- ev:
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out sending %+v through the bus", ev)
		}
	}
	send(ParticipantJoined{UserID: "user-1", DisplayName: "Alice"})
	send(TelemetryReceived{UserID: "user-1", Seq: 1, WordsCorrect: 1})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		log := bus.snapshot()
		if len(log) > 0 && log[len(log)-1].kind == "publish_room_closed" {
			if len(log) < 2 {
				t.Fatalf("expected at least one publish_out before room_closed, got %+v", log)
			}
			for _, entry := range log[:len(log)-1] {
				if entry.kind != "publish_out" {
					t.Fatalf("unexpected non-publish_out entry before room_closed: %+v", entry)
				}
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for publish_room_closed as the log's final entry, got %+v", bus.snapshot())
}
