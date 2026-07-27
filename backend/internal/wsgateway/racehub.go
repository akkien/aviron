package wsgateway

import (
	"context"
	"log/slog"
	"sync"

	"github.com/akkien/aviron/internal/roomrelay"
)

// connBufferSize bounds each connection's own outbound channel. A slow
// client's buffer filling up means that connection drops new broadcasts
// (see raceHub.run's non-blocking send) rather than ever stalling the
// shared fan-out loop or the bus subscription behind it.
const connBufferSize = 8

// raceHub fans a single race's room.{race_id}.out bus subscription
// (room-message-bus.md) out to every connection this gateway currently
// holds for that race — ws-gateway.md's gateway-side counterpart to
// room-service-adapter.md's drainBroadcast on the publishing side. Reused
// almost verbatim from the former in-process hub (which fanned
// RoomActor.Broadcast() directly, back when race-service held connections
// itself) — only the input source changed.
//
// Like RoomActor itself, raceHub uses a single goroutine (run) as the only
// mutator of its connection set, driven by channels rather than a mutex —
// consistent with this project's single-writer concurrency style.
type raceHub struct {
	register   chan chan []byte
	unregister chan chan []byte
	stop       chan struct{}
	closed     chan struct{}
	stopOnce   sync.Once
}

// newRaceHub starts fanning out's envelopes out until out closes, a
// room_closed envelope arrives, or stop is triggered — whichever happens
// first. onClose runs exactly once, after run's loop exits, so the caller
// (raceHubRegistry) can forget this hub instead of leaking a map entry.
// unsubscribe is called on every exit path, so this gateway never leaves a
// dangling room.{race_id}.out subscription behind.
func newRaceHub(out <-chan roomrelay.OutboundEnvelope, unsubscribe func(), onClose func()) *raceHub {
	h := &raceHub{
		register:   make(chan chan []byte),
		unregister: make(chan chan []byte),
		stop:       make(chan struct{}),
		closed:     make(chan struct{}),
	}
	go h.run(out, unsubscribe, onClose)
	return h
}

func (h *raceHub) run(out <-chan roomrelay.OutboundEnvelope, unsubscribe func(), onClose func()) {
	defer onClose()
	defer unsubscribe()
	defer close(h.closed)

	conns := make(map[chan []byte]struct{})
	for {
		select {
		case env, ok := <-out:
			if !ok {
				return
			}
			switch env.Kind {
			case roomrelay.OutboundKindBroadcast:
				for c := range conns {
					select {
					case c <- env.Payload:
					default:
						// That connection's buffer is full — drop for it
						// only; every other connection in the race still
						// gets this broadcast.
					}
				}
			case roomrelay.OutboundKindRoomClosed:
				// No drain loop needed here, unlike
				// room-service-adapter.md's own drainBroadcast on the
				// publishing side. That side's hazard is a genuine race
				// between two *independent* Go channels (broadcast and
				// ctx.Done()) becoming ready at the same moment, where
				// select's pseudo-random case choice could observe done
				// first and lose whatever was still buffered on the other
				// channel. This side only ever has one channel (out):
				// room_closed is just an ordinary envelope value delivered
				// through it, not a channel-close signal, and a single
				// publisher's messages arrive at a single subscriber in
				// the order they were sent (NATS's own ordering guarantee,
				// preserved end to end by roomrelay's own sequential
				// forwarding loop) — so every broadcast drainBroadcast
				// published before room_closed was necessarily already
				// received and fanned out by an earlier iteration of this
				// same loop, before this one ever saw room_closed at all.
				return
			}
		case c := <-h.register:
			conns[c] = struct{}{}
		case c := <-h.unregister:
			delete(conns, c)
		case <-h.stop:
			// Triggered by raceHubRegistry once this race's last local
			// connection detaches — nobody's left to deliver to, so
			// dropping whatever's still in flight is correct, not a bug
			// (unlike the room_closed case above, there's no "final
			// message" obligation once there are zero recipients).
			return
		}
	}
}

// signalStop triggers run's stop case exactly once — safe to call more
// than once (e.g. a concurrent detach racing run's own room_closed exit).
func (h *raceHub) signalStop() {
	h.stopOnce.Do(func() { close(h.stop) })
}

// registerConn attaches c to the fan-out. Safe to call even if the hub has
// already closed (room gone, or this gateway's last local connection for
// it already detached) — it just becomes a no-op instead of blocking
// forever on an unread channel.
func (h *raceHub) registerConn(c chan []byte) {
	select {
	case h.register <- c:
	case <-h.closed:
	}
}

// unregisterConn detaches c. Same closed-hub guard as registerConn.
func (h *raceHub) unregisterConn(c chan []byte) {
	select {
	case h.unregister <- c:
	case <-h.closed:
	}
}

// relayBus is the subset of *roomrelay.Bus's API this package depends on —
// satisfied by both it and *roomrelay.FakeBus, so tests can substitute a
// fake without a real NATS connection (mirrors internal/roombus's identical
// pattern for the same reason).
type relayBus interface {
	SubscribeOut(ctx context.Context, raceID string) (<-chan roomrelay.OutboundEnvelope, func(), error)
	PublishIn(ctx context.Context, raceID string, env roomrelay.InboundEnvelope) error
}

// raceHubEntry pairs a raceHub with how many local connections currently
// hold it attached.
type raceHubEntry struct {
	hub      *raceHub
	refCount int
}

// raceHubRegistry lazily creates one raceHub per race_id this gateway has
// at least one local connection for, reference-counted so a subscription
// is held open only for as long as it's actually needed — new relative to
// the former hubRegistry (which tied a hub's lifetime solely to the
// in-process RoomActor's own context, since every connection shared that
// one process). A gateway process serves many races across many local
// clients, so it should only hold a room.{race_id}.out subscription open
// for races it actually has a local connection for right now.
type raceHubRegistry struct {
	mu     sync.Mutex
	hubs   map[string]*raceHubEntry
	relay  relayBus
	ctx    context.Context
	logger *slog.Logger
}

// NewRaceHubRegistry constructs an empty raceHubRegistry. ctx is the
// gateway process's own root context — deliberately not a per-connection
// context, since a raceHub's subscription must outlive any single
// connection that happens to be the first or last to (dis)attach.
func NewRaceHubRegistry(ctx context.Context, relay relayBus, logger *slog.Logger) *raceHubRegistry {
	return &raceHubRegistry{hubs: make(map[string]*raceHubEntry), relay: relay, ctx: ctx, logger: logger}
}

// attach registers one more local connection's interest in raceID. On the
// first attach for a race, it subscribes to room.{race_id}.out
// synchronously, before returning — the same synchronous-before-return
// treatment room-service-adapter.md's feedInbox needed for the identical
// reason: NATS Core has no replay, so a broadcast published the instant
// this gateway's first local connection for a race attaches must not be
// able to race a subscription that hasn't actually reached the bus yet.
func (hr *raceHubRegistry) attach(raceID string) (*raceHub, error) {
	hr.mu.Lock()
	defer hr.mu.Unlock()

	if entry, ok := hr.hubs[raceID]; ok {
		entry.refCount++
		return entry.hub, nil
	}

	out, unsubscribe, err := hr.relay.SubscribeOut(hr.ctx, raceID)
	if err != nil {
		return nil, err
	}

	h := newRaceHub(out, unsubscribe, func() {
		hr.mu.Lock()
		delete(hr.hubs, raceID)
		hr.mu.Unlock()
	})
	hr.hubs[raceID] = &raceHubEntry{hub: h, refCount: 1}
	return h, nil
}

// detach reflects one local connection's departure from raceID. The last
// detach for a race unsubscribes from room.{race_id}.out — this gateway
// has no local clients left to deliver it to. Safe to call for a raceID
// with no entry (e.g. the room already closed and removed itself first
// via raceHub's own room_closed path) — a no-op, mirroring
// room.Registry.Remove's own double-removal tolerance.
func (hr *raceHubRegistry) detach(raceID string) {
	hr.mu.Lock()
	defer hr.mu.Unlock()

	entry, ok := hr.hubs[raceID]
	if !ok {
		return
	}
	entry.refCount--
	if entry.refCount <= 0 {
		delete(hr.hubs, raceID)
		entry.hub.signalStop()
	}
}
