package room

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// broadcastBufferSize is generous enough that a burst of ticks doesn't get
// dropped by RoomActor's non-blocking broadcast send before the WebSocket
// fan-out (websocket/ws-endpoint.md, not yet built) has a chance to drain it.
const broadcastBufferSize = 16

// heartbeatInterval is how often Spawn refreshes a room's Redis ownership
// claim (redis-room-registry.md) — well under roomlocator's own 60s claim
// TTL, so a normal GC pause or scheduling delay doesn't let the claim lapse.
// A var, not a const, so tests can shorten it (mirrors gracePeriodDuration
// in room.go).
var heartbeatInterval = 20 * time.Second

// RoomLocator makes this instance's room ownership durably visible to other
// instances via Redis (redis-room-registry.md, project-overview.md §5). A
// small structural interface, mirroring TickObserver, so internal/room
// never imports redis or internal/roomlocator directly.
type RoomLocator interface {
	Claim(ctx context.Context, raceID string) (bool, error)
	Refresh(ctx context.Context, raceID string) error
	Release(ctx context.Context, raceID string) error
}

// NoopLocator is the RoomLocator for single-instance dev/tests — every
// current NewRegistry(...) call site keeps working unchanged, with no real
// Redis dependency.
type NoopLocator struct{}

func (NoopLocator) Claim(ctx context.Context, raceID string) (bool, error) { return true, nil }
func (NoopLocator) Refresh(ctx context.Context, raceID string) error       { return nil }
func (NoopLocator) Release(ctx context.Context, raceID string) error       { return nil }

// RoomBus carries every real-time, room-scoped message to/from whichever
// process holds this room's WebSocket connections (room-message-bus.md,
// room-service-adapter.md). Defined here using only this package's own
// RoomEvent type, never internal/roomrelay's envelope types or
// internal/ws's protocol types directly: internal/ws already imports
// internal/room (for RoomEvent itself), so internal/room importing either
// package back would cycle. The concrete adapter that decodes raw client
// frames into RoomEvent — the only place that needs both — lives one level
// up, in internal/app.go's composition root, the same import-direction
// constraint kafka-consumer-postgres-sink.md's own composition already
// established a precedent for.
type RoomBus interface {
	// SubscribeIn returns raceID's inbound RoomEvents, already decoded,
	// plus an idempotent unsubscribe func. The returned channel closes once
	// ctx is done, the subscription ends, or unsubscribe is called —
	// whichever happens first.
	SubscribeIn(ctx context.Context, raceID string) (<-chan RoomEvent, func(), error)
	// PublishOut publishes an already-marshaled broadcast frame for raceID
	// — called once per message RoomActor.Broadcast() produces.
	PublishOut(ctx context.Context, raceID string, payload []byte) error
	// PublishRoomClosed signals raceID's room is finished. Always called
	// only after every PublishOut call for this raceID has already gone
	// out — see Spawn's drainBroadcast.
	PublishRoomClosed(ctx context.Context, raceID string) error
}

// NoopRoomBus is the RoomBus for tests and any Registry with nothing real
// behind it — mirrors NoopLocator/NoopPublisher exactly. SubscribeIn hands
// back an already-closed channel (nothing ever arrives, so nothing can
// leak), PublishOut/PublishRoomClosed are no-ops.
type NoopRoomBus struct{}

func (NoopRoomBus) SubscribeIn(ctx context.Context, raceID string) (<-chan RoomEvent, func(), error) {
	ch := make(chan RoomEvent)
	close(ch)
	return ch, func() {}, nil
}

func (NoopRoomBus) PublishOut(ctx context.Context, raceID string, payload []byte) error { return nil }
func (NoopRoomBus) PublishRoomClosed(ctx context.Context, raceID string) error          { return nil }

// Registry maps a race_id to the RoomActor currently running that race. It
// only ever holds *RoomActor pointers and dispatches to them — it never
// reads or writes a RoomActor's participants directly, preserving the
// single-writer principle from room-actor-core.md. A sync.RWMutex guards
// this instance's own local map; cross-instance visibility of who owns
// which room is locator's job (redis-room-registry.md), not this map's.
type Registry struct {
	mu           sync.RWMutex
	rooms        map[string]*RoomActor
	logger       *slog.Logger
	tickObserver TickObserver
	locator      RoomLocator
	publisher    EventPublisher
	bus          RoomBus
}

// NewRegistry constructs an empty Registry. logger is the process-wide
// logger; Spawn tags a race_id-scoped child logger off it for each room
// actor it constructs, rather than threading race_id through every log call
// site inside internal/room. tickObserver, unlike logger, is passed to
// every room actor unchanged (not per-race-tagged): it's a single
// process-wide metrics sink (prometheus-metrics.md), not something that
// needs a race_id attribute the way a log line does. locator is NoopLocator
// for single-instance dev/tests, or a real *roomlocator.Locator once
// running multiple instances. publisher is likewise a single process-wide
// dependency (kafka-producer.md) — NoopPublisher for no-Kafka local dev, or
// a real *kafka.Producer.
// bus is likewise a single process-wide dependency (room-message-bus.md,
// room-service-adapter.md) — NoopRoomBus for tests, or the real adapter
// internal/app.go composes around *roomrelay.Bus.
func NewRegistry(logger *slog.Logger, tickObserver TickObserver, locator RoomLocator, publisher EventPublisher, bus RoomBus) *Registry {
	return &Registry{rooms: make(map[string]*RoomActor), logger: logger, tickObserver: tickObserver, locator: locator, publisher: publisher, bus: bus}
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
	roomLogger := reg.logger.With(slog.String("race_id", raceID))
	actor := NewRoomActor(ctx, raceID, distanceMeters, broadcast, finisher, leaver, canceller, reg.publisher, roomLogger, reg.tickObserver)

	reg.mu.Lock()
	reg.rooms[raceID] = actor
	reg.mu.Unlock()

	if _, err := reg.locator.Claim(ctx, raceID); err != nil {
		roomLogger.Error("roomlocator: claim failed", slog.Any("error", err))
	}

	// SubscribeIn is called synchronously, here, rather than inside
	// feedInbox's own goroutine: NATS Core has no message replay
	// (room-message-bus.md), so if the subscription weren't established
	// until some arbitrary later moment on a background goroutine, a
	// caller could publish a race's very first join_race the instant Spawn
	// returns and lose it to a subscription that hadn't actually reached
	// the bus yet — the same synchronous-before-return treatment Claim
	// above already gets, for the same reason.
	sub, unsubscribeIn, err := reg.bus.SubscribeIn(ctx, raceID)
	if err != nil {
		roomLogger.Error("roombus: subscribe in failed", slog.Any("error", err))
	}

	go actor.Run()
	go reg.cleanupWhenDone(raceID, actor)
	if err == nil {
		go reg.feedInbox(sub, unsubscribeIn, actor)
	}
	go reg.drainBroadcast(actor.Context(), raceID, actor, roomLogger)
	// heartbeatInterval is read here, synchronously on the caller's
	// goroutine, and passed in rather than read inside the heartbeat
	// goroutine itself — mirrors NewRoomActor reading noShowTimeoutDuration
	// synchronously during construction, so a test overriding the var via
	// withShortHeartbeatInterval can never race a still-running heartbeat
	// goroutine left over from a previous test's Spawn.
	go reg.heartbeat(actor.Context(), raceID, roomLogger, heartbeatInterval)
	return actor
}

// heartbeat periodically refreshes raceID's Redis ownership claim
// (redis-room-registry.md) for as long as actor's context stays open,
// stopping the instant it's done — no new lifecycle primitive beyond the
// context Spawn already threads through the room actor.
func (reg *Registry) heartbeat(ctx context.Context, raceID string, roomLogger *slog.Logger, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := reg.locator.Refresh(ctx, raceID); err != nil {
				roomLogger.Error("roomlocator: refresh failed", slog.Any("error", err))
			}
		case <-ctx.Done():
			return
		}
	}
}

// feedInbox feeds every already-decoded RoomEvent arriving on sub into
// actor's single-writer inbox via the same Send entry point every other
// caller already uses. sub is subscribed synchronously by Spawn, before it
// returns (see the comment there) — this goroutine only drains it and
// unsubscribes once done. A plain range is enough — sub's channel already
// closes once ctx is done, the subscription ends, or unsubscribe is
// called, mirroring roomlocator.SubscribeRoomEvents — no separate
// select-on-ctx.Done() needed in this loop.
func (reg *Registry) feedInbox(sub <-chan RoomEvent, unsubscribe func(), actor *RoomActor) {
	defer unsubscribe()

	for ev := range sub {
		actor.Send(ev)
	}
}

// drainBroadcast publishes every message the room actor broadcasts onto
// the bus, then — once actor's context is done AND every already-buffered
// broadcast has actually been published — publishes room_closed exactly
// once. actor.Broadcast()'s channel is never closed by RoomActor itself
// (its shutdown signal is always ctx, not a channel close), so a plain
// `for range actor.Broadcast()` would hang forever and never reach the
// room_closed publish. This mirrors internal/wsgateway/hub.go's hub.run
// done case exactly: broadcast (still carrying the final
// race_state/race_finished) and ctx.Done() (just cancelled) become ready
// at essentially the same moment, and select picks a ready case
// pseudo-randomly, not in send order — draining explicitly here guarantees
// delivery instead of losing whatever was still buffered.
func (reg *Registry) drainBroadcast(ctx context.Context, raceID string, actor *RoomActor, roomLogger *slog.Logger) {
	broadcast := actor.Broadcast()
	for {
		select {
		case msg := <-broadcast:
			if err := reg.bus.PublishOut(ctx, raceID, msg); err != nil {
				roomLogger.Error("roombus: publish out failed", slog.Any("error", err))
			}
		case <-ctx.Done():
			// ctx is cancelled from here on, so every remaining publish —
			// including room_closed — uses a fresh background context
			// instead, mirroring cleanupWhenDone's own precedent for
			// Release: Bus.PublishOut checks ctx.Err() first and would
			// otherwise silently drop exactly the messages this drain
			// exists to deliver.
			for {
				select {
				case msg := <-broadcast:
					if err := reg.bus.PublishOut(context.Background(), raceID, msg); err != nil {
						roomLogger.Error("roombus: publish out failed", slog.Any("error", err))
					}
				default:
					if err := reg.bus.PublishRoomClosed(context.Background(), raceID); err != nil {
						roomLogger.Error("roombus: publish room_closed failed", slog.Any("error", err))
					}
					return
				}
			}
		}
	}
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

	// actor.Context() is already Done here, so a fresh context.Background()
	// is used for this one last Redis call rather than one derived from it.
	if err := reg.locator.Release(context.Background(), raceID); err != nil {
		reg.logger.Error("roomlocator: release failed", slog.String("race_id", raceID), slog.Any("error", err))
	}
}

// Get returns the RoomActor currently running raceID, if any.
func (reg *Registry) Get(raceID string) (*RoomActor, bool) {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	actor, ok := reg.rooms[raceID]
	return actor, ok
}

// Count returns the number of rooms currently running (prometheus-metrics.md
// — active room count). Safe to call from the metrics scrape goroutine: it
// only takes the same RLock every other read-only Registry method already
// does.
func (reg *Registry) Count() int {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	return len(reg.rooms)
}

// InboxBufferUsage and BroadcastBufferUsage sum how many messages are
// currently queued in every live room's inbox/broadcast channel
// (prometheus-metrics.md — channel buffer usage). A snapshot at scrape
// time, not a running total: RLock only guards reg.rooms itself, not each
// RoomActor's channels, which len() already reads safely without it.
func (reg *Registry) InboxBufferUsage() int {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	total := 0
	for _, actor := range reg.rooms {
		total += actor.InboxLen()
	}
	return total
}

func (reg *Registry) BroadcastBufferUsage() int {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	total := 0
	for _, actor := range reg.rooms {
		total += actor.BroadcastLen()
	}
	return total
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
