package room

import (
	"context"
	"sync"
)

// broadcastBufferSize is generous enough that a burst of ticks doesn't get
// dropped by RoomActor's non-blocking broadcast send before the WebSocket
// fan-out (websocket/ws-endpoint.md, not yet built) has a chance to drain it.
const broadcastBufferSize = 16

// Registry maps a race_id to the RoomActor currently running that race. It
// only ever holds *RoomActor pointers and dispatches to them — it never
// reads or writes a RoomActor's participants directly, preserving the
// single-writer principle from room-actor-core.md. A sync.RWMutex is enough
// for this single-instance phase; the Redis-backed cross-instance registry
// is Phase 4's concern, not this one's.
type Registry struct {
	mu    sync.RWMutex
	rooms map[string]*RoomActor
}

// NewRegistry constructs an empty Registry.
func NewRegistry() *Registry {
	return &Registry{rooms: make(map[string]*RoomActor)}
}

// Spawn constructs a RoomActor for raceID seeded with the race's
// distanceMeters, registers it, and starts its Run loop. ctx should outlive
// the caller (e.g. the process's root context), not a per-request context,
// since the room actor must keep running after the request that triggered
// the spawn has returned. finisher persists results once the race completes
// (race-completion/finish-race.md), leaver persists a pending-race
// participant intentionally leaving (pending-connections.md), and canceller
// persists a pending race's cancellation once the room tears down before
// ever going active (room-lifecycle/cancelled-race-status.md) — typically
// all three the same concrete *race.RaceService in three structural roles,
// passed in by the caller rather than threaded through this Registry's own
// construction, to avoid reordering how internal/app.go wires the
// composition root.
func (reg *Registry) Spawn(ctx context.Context, raceID string, distanceMeters int, finisher RaceFinisher, leaver RaceLeaver, canceller RaceCanceller) *RoomActor {
	broadcast := make(chan []byte, broadcastBufferSize)
	actor := NewRoomActor(ctx, raceID, distanceMeters, broadcast, finisher, leaver, canceller)

	reg.mu.Lock()
	reg.rooms[raceID] = actor
	reg.mu.Unlock()

	go actor.Run()
	go reg.cleanupWhenDone(raceID, actor)
	return actor
}

// cleanupWhenDone removes raceID's entry once actor's context is done,
// regardless of why — an explicit Remove call, or (race-completion/finish-race.md)
// the actor cancelling itself once a race finishes or a room is abandoned.
// Without this, a self-cancelled actor would leave a stale *RoomActor
// pointing at a dead goroutine in the map forever, since Remove was
// previously the only path that ever deleted an entry. Mirrors
// internal/ws's hubRegistry, which already uses the same
// watch-context-then-self-cleanup shape for its own map.
func (reg *Registry) cleanupWhenDone(raceID string, actor *RoomActor) {
	<-actor.Context().Done()
	reg.mu.Lock()
	if reg.rooms[raceID] == actor {
		delete(reg.rooms, raceID)
	}
	reg.mu.Unlock()
}

// Get returns the RoomActor currently running raceID, if any.
func (reg *Registry) Get(raceID string) (*RoomActor, bool) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	actor, ok := reg.rooms[raceID]
	return actor, ok
}

// Remove stops raceID's RoomActor and deregisters it. A raceID with no
// registered actor is a no-op, so callers don't need to guard against
// double-removal (e.g. finish-race and grace-period expiry racing each
// other).
func (reg *Registry) Remove(raceID string) {
	reg.mu.Lock()
	actor, ok := reg.rooms[raceID]
	if ok {
		delete(reg.rooms, raceID)
	}
	reg.mu.Unlock()

	if ok {
		actor.cancel()
	}
}
