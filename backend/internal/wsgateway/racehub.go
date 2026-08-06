package wsgateway

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"

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
// connRegistration pairs one connection's outbound fan-out channel with
// the context.CancelFunc that ends that connection's readLoop/writeLoop
// (serveConn's own connCtx). Tracked so a raceHub can force-disconnect
// every local connection it holds on process shutdown
// (graceful-shutdown.md's RaceHubRegistry.Shutdown), not just when the
// room itself ends or a connection detaches on its own.
type connRegistration struct {
	ch     chan []byte
	cancel context.CancelFunc
}

type raceHub struct {
	register   chan connRegistration
	unregister chan chan []byte
	stop       chan struct{}
	shutdown   chan struct{}
	closed     chan struct{}
	stopOnce   sync.Once
	// connCount mirrors conns' length inside run's own single-writer loop
	// (updated there, alongside the map itself) so Count can be read from a
	// metrics scrape goroutine without querying through run's select loop
	// under load — the same atomic.Int64-over-query-through-channel
	// tradeoff prometheus-metrics.md already reasoned through once for the
	// original (removed) connection gauge, reused here rather than
	// re-litigated (metrics/metrics-parity.md).
	connCount atomic.Int64
}

// newRaceHub starts fanning out's envelopes out until out closes, a
// room_closed envelope arrives, or stop is triggered — whichever happens
// first. onClose runs exactly once, after run's loop exits, so the caller
// (RaceHubRegistry) can forget this hub instead of leaking a map entry.
// unsubscribe is called on every exit path, so this gateway never leaves a
// dangling room.{race_id}.out subscription behind.
func newRaceHub(raceID string, out <-chan roomrelay.OutboundEnvelope, unsubscribe func(), onClose func(), logger *slog.Logger) *raceHub {
	h := &raceHub{
		register:   make(chan connRegistration),
		unregister: make(chan chan []byte),
		stop:       make(chan struct{}),
		shutdown:   make(chan struct{}),
		closed:     make(chan struct{}),
	}
	go h.run(raceID, out, unsubscribe, onClose, logger)
	return h
}

func (h *raceHub) run(raceID string, out <-chan roomrelay.OutboundEnvelope, unsubscribe func(), onClose func(), logger *slog.Logger) {
	defer onClose()
	defer unsubscribe()
	defer close(h.closed)

	conns := make(map[chan []byte]context.CancelFunc)
	for {
		select {
		case env, ok := <-out:
			if !ok {
				return
			}
			logger.Info("wsgateway: received", slog.String("race_id", raceID), slog.String("kind", string(env.Kind)))
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
		case reg := <-h.register:
			conns[reg.ch] = reg.cancel
			h.connCount.Store(int64(len(conns)))
		case c := <-h.unregister:
			delete(conns, c)
			h.connCount.Store(int64(len(conns)))
		case <-h.shutdown:
			// Force-disconnects every connection this hub currently holds
			// — used only when the whole gateway process is shutting down
			// (graceful-shutdown.md's RaceHubRegistry.Shutdown), never as
			// part of this hub's own normal per-race lifecycle. Cancelling
			// a connection's own ctx (not closing h.closed) means its
			// readLoop takes the same path a real network disconnect
			// would — publishing InboundKindDisconnected — which is
			// exactly right here: from the room's perspective this
			// participant really is disconnecting (this gateway is going
			// away), unlike the room_closed case above where the room
			// already told everyone it's finished.
			for _, cancel := range conns {
				cancel()
			}
		case <-h.stop:
			// Triggered by RaceHubRegistry once this race's last local
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

// Count returns how many local connections this hub currently holds. Safe
// to call from any goroutine, including a metrics scrape — reads an atomic
// updated inline by run's own register/unregister handling, not a query
// through run's select loop.
func (h *raceHub) Count() int {
	return int(h.connCount.Load())
}

// registerConn attaches c to the fan-out, tracking cancel (serveConn's own
// connCtx cancel func) so disconnectAll can force this connection closed
// later if the whole gateway process shuts down. Safe to call even if the
// hub has already closed (room gone, or this gateway's last local
// connection for it already detached) — it just becomes a no-op instead
// of blocking forever on an unread channel.
func (h *raceHub) registerConn(c chan []byte, cancel context.CancelFunc) {
	select {
	case h.register <- connRegistration{ch: c, cancel: cancel}:
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

// disconnectAll signals run to force-cancel every locally registered
// connection's context (graceful-shutdown.md). Fire-and-forget — it
// doesn't wait for those connections to actually finish closing;
// RaceHubRegistry.Shutdown's own caller doesn't need it to, since
// http.Server.Shutdown already waits for each affected ServeHTTP call to
// return on its own. Same closed-hub guard as registerConn/unregisterConn.
func (h *raceHub) disconnectAll() {
	select {
	case h.shutdown <- struct{}{}:
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

// RaceHubRegistry lazily creates one raceHub per race_id this gateway has
// at least one local connection for, reference-counted so a subscription
// is held open only for as long as it's actually needed — new relative to
// the former hubRegistry (which tied a hub's lifetime solely to the
// in-process RoomActor's own context, since every connection shared that
// one process). A gateway process serves many races across many local
// clients, so it should only hold a room.{race_id}.out subscription open
// for races it actually has a local connection for right now.
type RaceHubRegistry struct {
	mu     sync.Mutex
	hubs   map[string]*raceHubEntry
	relay  relayBus
	ctx    context.Context
	logger *slog.Logger
}

// NewRaceHubRegistry constructs an empty RaceHubRegistry. ctx is the
// gateway process's own root context — deliberately not a per-connection
// context, since a raceHub's subscription must outlive any single
// connection that happens to be the first or last to (dis)attach.
func NewRaceHubRegistry(ctx context.Context, relay relayBus, logger *slog.Logger) *RaceHubRegistry {
	return &RaceHubRegistry{hubs: make(map[string]*raceHubEntry), relay: relay, ctx: ctx, logger: logger}
}

// attach registers one more local connection's interest in raceID. On the
// first attach for a race, it subscribes to room.{race_id}.out
// synchronously, before returning — the same synchronous-before-return
// treatment room-service-adapter.md's feedInbox needed for the identical
// reason: NATS Core has no replay, so a broadcast published the instant
// this gateway's first local connection for a race attaches must not be
// able to race a subscription that hasn't actually reached the bus yet.
func (hr *RaceHubRegistry) attach(raceID string) (*raceHub, error) {
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

	h := newRaceHub(raceID, out, unsubscribe, func() {
		hr.mu.Lock()
		delete(hr.hubs, raceID)
		hr.mu.Unlock()
	}, hr.logger)
	hr.hubs[raceID] = &raceHubEntry{hub: h, refCount: 1}
	return h, nil
}

// detach reflects one local connection's departure from raceID. The last
// detach for a race unsubscribes from room.{race_id}.out — this gateway
// has no local clients left to deliver it to. Safe to call for a raceID
// with no entry (e.g. the room already closed and removed itself first
// via raceHub's own room_closed path) — a no-op, mirroring
// room.Registry.Remove's own double-removal tolerance.
func (hr *RaceHubRegistry) detach(raceID string) {
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

// Count sums the number of local connections held across every race this
// gateway currently has at least one connection for
// (metrics/metrics-parity.md — aviron_ws_connections_active). Safe to call
// from a metrics scrape goroutine: takes the same mutex every other
// read-only method already does, and each hub's own Count is an atomic
// read, not a query through that hub's run loop.
func (hr *RaceHubRegistry) Count() int {
	hr.mu.Lock()
	defer hr.mu.Unlock()

	total := 0
	for _, entry := range hr.hubs {
		total += entry.hub.Count()
	}
	return total
}

// Shutdown force-disconnects every locally-held connection across every
// race this gateway currently has at least one connection for
// (graceful-shutdown.md). Called once, during process shutdown — never
// as part of this registry's normal per-race attach/detach lifecycle.
// Each affected hub's own refcount/stop bookkeeping still runs its normal
// course afterward: as each disconnected connection's serveConn returns,
// it calls detach exactly like a real disconnect would, tearing the hub
// down once its last connection is gone.
func (hr *RaceHubRegistry) Shutdown() {
	hr.mu.Lock()
	hubs := make([]*raceHub, 0, len(hr.hubs))
	for _, entry := range hr.hubs {
		hubs = append(hubs, entry.hub)
	}
	hr.mu.Unlock()

	for _, hub := range hubs {
		hub.disconnectAll()
	}
}
