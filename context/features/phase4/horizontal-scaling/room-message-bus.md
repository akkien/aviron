# Room Message Bus (`internal/roomrelay`)

## Overview

The new component this whole revision is really about: a transport
carrying every real-time, room-scoped message between `ws-gateway`
(holds the connection) and `race-service` (runs the `RoomActor`) — the
two are never the same process anymore, so nothing crosses between them
except through this bus. Per `docs/knowledge-summary.md`'s "Revision:
adopting a WS Gateway tier", this is the generalized revival of
`cross-instance-relay.md`'s original design (written once, superseded by
`race-router.md` before it was ever built, now actually being
implemented) — its own self-assessment, *"the hardest and highest-risk
spec in the entire project,"* still applies. Built before `ws-gateway.md`
and `room-service-adapter.md` because both are independent consumers of
the envelope format this spec fixes.

**Correction (this pass): NATS Core, not Redis pub/sub.** This spec
originally chose Redis pub/sub for the bus transport, reusing the Redis
instance `redis-room-registry.md` already runs. This revision switches
the transport to NATS Core instead — a separate, dedicated messaging
system, not an additional feature bolted onto a cache/data-store like
Redis. See `docs/knowledge-summary.md`'s "## Game Message Bus" section
for the full comparison this decision is based on; the short version: at
this project's scale either would work correctly, but NATS is
purpose-built for exactly this traffic shape (transient, high-frequency,
fan-out pub/sub, no persistence needed) and isolates it from Redis's own
load (the registry's `Owner()` lookups and the evicted-reconnect check
below both still hit Redis on every connection attempt — a burst of
real-time room traffic no longer has any way to contend with that path).
**The room registry itself (`redis-room-registry.md`) stays on Redis,
unchanged** — this is a transport swap for the message bus specifically,
not a wholesale move off Redis. Matches the split the project's own
research landed on: Redis for the registry/cache/sessions, NATS for
realtime gameplay messaging, Kafka for durable business events.

## Current state (confirmed by reading the code)

No `internal/roomrelay` package exists yet. `internal/roomlocator`
exists and owns a completely different concern (`SET`/`GET`/`EXPIRE` on
`room:<id>` keys, plus the small, low-frequency `room:events` Redis
pub/sub channel for cache invalidation — `redis-room-registry.md`) on a
**different** piece of infrastructure than this spec now targets. The
two packages don't share a client or a connection: `roomlocator` wraps
Redis, `roomrelay` (this spec) wraps NATS. Both are consulted by the same
processes (`ws-gateway`, `race-service`), just for different reasons —
see `ws-gateway.md`'s Config for how a single process ends up holding
both a `RedisURL` and a `NATSURL`.

## Subject design

Two NATS subjects per race, named directly off `race_id` — no separate
lookup step needed to know which subject a message for a given race
belongs on, unlike `roomlocator.Owner()`'s instance lookup. NATS subjects
are dot-delimited (not colon-delimited like the Redis channel names this
spec originally used) — renamed accordingly:

| Subject | Direction | Carries |
| --- | --- | --- |
| `room.{race_id}.in` | `ws-gateway` → owning `race-service` | Every client-sent frame (`join_race`/`telemetry`/`leave_race`) plus gateway-detected disconnects |
| `room.{race_id}.out` | Owning `race-service` → every `ws-gateway` with a local client in this race | Every broadcast `RoomActor` already produces (`race_state`, `race_started`, `race_finished`), plus one `room_closed` signal when the room's context ends |

**Plain subscriptions, deliberately not NATS Queue Groups.** NATS'
Queue Groups give exactly-one-subscriber delivery (load-balanced across
a group) — the wrong semantics for `room.{race_id}.out`, which needs
*every* subscribed `ws-gateway` to receive *every* message (fan-out, not
load-balancing, since each gateway is forwarding to a disjoint set of
locally-held connections). `room.{race_id}.in` never has more than one
subscriber anyway (only the owning instance ever subscribes), so the
question doesn't even arise there. Both directions use a plain
`nc.Subscribe`/`nc.ChanSubscribe`, never `nc.QueueSubscribe` — worth
stating explicitly since NATS' own docs default to showing Queue Groups
for load-distribution use cases, and picking the wrong one here would
silently drop most gateways' traffic.

**Wildcard subjects considered, not used.** NATS supports hierarchical
wildcard subscriptions (`room.*.out`, `room.>`) that Redis's flat channel
namespace can't match as naturally (though Redis's own `PSUBSCRIBE`
glob patterns get partway there). A single `ws-gateway` process could
subscribe once to `room.*.out` instead of dynamically subscribing per
race — but that means receiving (and filtering, in Go code) every other
race's traffic too, the opposite of "only pay for what a gateway actually
has local clients for." Per-race dynamic subscription (see "Subscription
lifecycle" below) stays the design; wildcards are noted here as an
option that exists, not one this spec takes.

## Payload envelopes

**Unchanged from the Redis-based version of this spec** — the envelope
types are transport-agnostic Go structs, marshaled to JSON either way;
switching the transport underneath them required no changes to either
type:

### Inbound (`room.{race_id}.in`)

```go
package roomrelay

type InboundKind string

const (
    InboundKindMessage      InboundKind = "message"      // a client-sent frame
    InboundKindDisconnected InboundKind = "disconnected"  // gateway's reader loop detected the local socket closed
)

// InboundEnvelope is published by ws-gateway, once per client-sent frame
// or detected disconnect. UserID/DisplayName are attached by the gateway
// (already known from session-token verification at connect time — see
// ws-gateway.md) rather than trusted from the message body itself, the
// same trust boundary internal/ws.readLoop already enforces today by
// calling toRoomEvent(userID, displayName) with values it derived itself,
// never values parsed out of the client's JSON.
type InboundEnvelope struct {
    Kind        InboundKind     `json:"kind"`
    RaceID      string          `json:"race_id"`
    UserID      string          `json:"user_id"`
    DisplayName string          `json:"display_name,omitempty"`
    // Message is the raw client JSON frame, present only when
    // Kind == InboundKindMessage — deliberately kept as the exact bytes
    // ws.decodeClientMessage already knows how to parse, so
    // room-service-adapter.md's consumer can reuse
    // ws.ClientMessage.toRoomEvent(userID, displayName) completely
    // unchanged instead of inventing a second wire format for the same
    // three message types.
    Message json.RawMessage `json:"message,omitempty"`
}
```

Reusing `ws.ClientMessage`/`toRoomEvent` as the inner payload (rather than
designing a new bus-native event schema) is the one design choice this
spec leans on hardest: `internal/ws/protocol.go`'s decode/convert logic
doesn't change at all under this revision, it just gets called from a
bus-message handler (`room-service-adapter.md`) instead of a WS-reader
goroutine. `InboundKindDisconnected` is the one case with no client
frame behind it at all — the gateway's reader loop synthesizes it
directly when a read fails, mirroring exactly what `readLoop` does
today (`actor.Send(room.ParticipantDisconnected{UserID: userID})`, just
over the bus instead of a local call).

### Outbound (`room.{race_id}.out`)

```go
type OutboundKind string

const (
    OutboundKindBroadcast   OutboundKind = "broadcast"    // forward Payload verbatim to every local connection in this race
    OutboundKindRoomClosed  OutboundKind = "room_closed"    // this race is over — close every local connection, tear down local state
)

// OutboundEnvelope is published by the owning race-service instance.
type OutboundEnvelope struct {
    Kind    OutboundKind    `json:"kind"`
    RaceID  string          `json:"race_id"`
    // Payload is the exact []byte RoomActor.Broadcast() already produces
    // today (race_state/race_started/race_finished, already-marshaled
    // client-facing JSON) — present only when Kind == OutboundKindBroadcast.
    // No re-encoding happens at this layer; the bus is a transparent pipe
    // for bytes the room actor already knows how to build.
    Payload json.RawMessage `json:"payload,omitempty"`
}
```

**Deliberately no per-user eviction message on this channel** — see
"What this reintroduces, honestly" in `docs/knowledge-summary.md`'s
Revision subsection for why `IsEvicted` never needs to force-close an
already-live connection in this codebase's actual semantics.
`OutboundKindRoomClosed` is the real "force-close active connections"
case, a room-lifecycle signal, mirroring `internal/ws/hub.go`'s existing
`done`/`hub.closed` behavior, just crossing a process boundary now.

## Evicted-reconnect checks bypass the bus entirely

Unchanged by the NATS switch — this was never part of the bus's job.
`IsEvicted` doesn't need a request/reply round trip (NATS Core *does*
support native request/reply, unlike Redis pub/sub, but that's not a
reason to route this check through the bus either): `race-service`
writes `SADD race:{race_id}:evicted {user_id}` to Redis at the exact
point `RoomActor.departParticipant` already marks someone evicted, and
`ws-gateway` does a plain `SISMEMBER race:{race_id}:evicted {user_id}`
synchronously, once, at connection-attempt time (see `ws-gateway.md`'s
connection-establishment sequence). Cheaper than a bus round trip either
way, and it sidesteps the cold-start gap a message-based approach would
have: a gateway that's never had a local client for this race before
(never subscribed to `room.{race_id}.out`, never would have seen a past
event) still gets the right answer, because it's a direct read against
durable state, not something it had to have been listening for.

## Subscription lifecycle

**`race-service` side (`room-service-adapter.md`):** subscribes to
`room.{race_id}.in` at the exact moment it claims ownership (`Locator.
Claim` succeeds, immediately before `RoomActor.Run()` starts) and
unsubscribes (`sub.Unsubscribe()`) when the room's context ends (same
moment `Locator.Release` already fires) — one subscriber goroutine per
room, same lifetime as the `RoomActor` itself.

**`ws-gateway` side:** subscribes to `room.{race_id}.out` when its
*first* local client for that race connects, unsubscribes when its
*last* local client for that race disconnects (leaves, or grace-period
evicts — from the gateway's perspective, "no more locally-held sockets
for this `race_id`") — reference-counted, so a gateway pod never holds a
subscription open for a race nobody currently connected to it cares
about. See `ws-gateway.md`'s "Local per-race state" for the exact
bookkeeping.

## Concurrency

- Both directions use `nats.go`'s `Subscribe`/`ChanSubscribe` (a
  receiving goroutine or channel per subscribed subject) — no shared
  mutable state inside `internal/roomrelay` itself beyond what a single
  subscription's own receive loop touches, matching the single-writer
  principle this project already applies everywhere state is shared
  across goroutines. `internal/roomrelay`'s own public API
  (`SubscribeIn`/`SubscribeOut`/`PublishIn`/`PublishOut`, each still
  returning/accepting `<-chan Envelope`) is unchanged in shape from the
  Redis-based version — `ws-gateway.md` and `room-service-adapter.md`
  consume this package's Go-level API, not its transport directly, so
  neither of those specs needed any changes as a result of this switch.
- `nc.Publish` is safe for concurrent use from multiple goroutines — the
  shared `*nats.Conn` already guarantees this, no additional locking
  needed in `internal/roomrelay` itself, same property `*redis.Client`
  had.
- `go test -race` coverage: a fake in-memory bus (mirroring this
  project's existing fake-repository/fake-locator testing convention —
  no real NATS needed for unit tests) exercising concurrent
  publish-while-subscribing, and subscribe/unsubscribe racing a publish
  landing mid-transition.

## Notes

- **At-most-once delivery, accepted deliberately — same tradeoff
  category as the Redis-based version, not a regression.** NATS Core (no
  JetStream) has no message durability and no replay, same as Redis
  pub/sub did: a message published while zero processes are subscribed
  to that subject is simply lost, and a subscriber that briefly drops
  never gets a replay of what it missed. Accepted for the same reasons
  as before: `race_state` broadcasts repeat every 250ms — a single
  dropped tick self-heals at the next one — and `room_closed`/the initial
  `join_race`/`telemetry` frames already have a natural retry path via
  this project's existing reconnect-with-grace-period design
  (`project-overview.md` §4.3).
- **One real advantage over the Redis-based version: the upgrade path to
  durability is native, not a system swap.** If this tradeoff ever needs
  revisiting, NATS JetStream is a mode of the *same* NATS deployment
  (persistent streams, replay, at-least-once/exactly-once delivery
  options) — no second messaging system to introduce, unlike upgrading
  off Redis pub/sub would have required. Not pursued now — no
  requirement in this project's scope needs it — but worth recording as
  the concrete next step if `multi-instance-dev-setup.md`'s kill-test
  (the "silent hang after owner crash" gap that spec already flagged)
  ends up needing message replay rather than a staleness-timeout fix.
- **Single NATS instance for now**, the same disclosed simplification
  `redis-room-registry.md` already carries for Redis — a real deployment
  of this design should run NATS clustered for HA; this project runs one
  instance, deliberately, for local-dev simplicity. Same category of
  accepted single-point-of-failure risk this project already carries for
  Postgres and Redis.
- **This is genuinely the highest-risk spec in this phase, same as its
  predecessor.** `ws-gateway.md`'s and `room-service-adapter.md`'s own
  Testing sections both need a real integration test exercising this bus
  end to end (publish on one side, assert receipt and correct effect on
  the other) against a real (or embedded, via `github.com/nats-io/
  nats-server`'s in-process test server — the NATS ecosystem's own
  equivalent of `miniredis`) NATS instance before `multi-instance-dev-
  setup.md`'s manual verification — don't treat either side's unit tests
  (against a fake `roomrelay`) as sufficient proof the wire format
  actually round-trips correctly between two real processes.
