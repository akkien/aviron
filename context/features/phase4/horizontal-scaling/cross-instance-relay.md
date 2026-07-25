# Cross-Instance Relay

## Overview

**This is the hardest and highest-risk spec in the entire project.**
`redis-room-registry.md` lets any instance answer "who owns this room?" —
this spec is what actually makes a client's traffic work when the answer
isn't "me." Per `context/project-overview.md` §5: "every instance
publishes/subscribes to a Redis pub/sub channel keyed by `room_id`, so a
client can connect to any instance and that instance just forwards
messages via Redis to whichever instance currently owns the room."

Read this whole spec, including every "open question for `load`/`start`"
below, before writing code — more than any other spec in this project, the
risk here is a subtle ordering or lifecycle bug that only shows up under
real two-instance load, in exactly the class of bug
`docs/concurrency.md`'s existing writeup (the finish-race cascading
disconnect) already shows this codebase is capable of producing when two
independent `select`s race against each other. Budget time to re-read that
doc before starting.

## The three call sites that currently assume "local or nonexistent"

Confirmed by reading the code directly, not assumed:

1. **`internal/ws/endpoint.go`, `ServeHTTP`** — `h.registry.Get(raceID)` on
   a miss returns `404 race not found`. Wrong once the race exists on a
   different instance. Also calls `actor.IsEvicted(userID)` — a
   synchronous, in-process query — before accepting the WS upgrade.
2. **`internal/ws/endpoint.go`, `readLoop`** — every decoded client message
   becomes `actor.Send(ev)` — a direct in-process call. If this
   connection's own instance doesn't own the room (see relay design
   below), there is no local `actor` to call `Send` on in the first place.
3. **`internal/race/handler.go`, `Start`** — `h.registry.Get(raceID)` to
   call `actor.MarkActive(promptText)`, after `RaceService.StartRace` has
   already durably flipped `races.status` in Postgres. Same miss problem
   as #1, for a REST call instead of a WS one.

And the reverse direction — once a client *is* attached to a non-owning
instance, that instance also needs the room's outbound
`race_state`/`race_finished`/`race_started`/`race_expired` broadcasts to
reach it, which today only flow through the owning instance's own
`hub` (`internal/ws/hub.go`), fed directly from `RoomActor.Broadcast()`.

## Design

### Two Redis pub/sub channels per room

- `room:<raceID>:in` — client-originated events. Only the **owning**
  instance subscribes (one subscription per locally-running room, started
  in `Registry.Spawn` alongside the existing heartbeat/cleanup goroutines,
  stopped via the same `actor.Context()`). Any instance — owning or not —
  publishes here whenever it needs to deliver an event to this room.
- `room:<raceID>:out` — the room's outbound broadcast bytes, verbatim
  (the same `[]byte` `RoomActor.Broadcast()` already produces — no
  re-encoding). The owning instance publishes every broadcast here **in
  addition to** feeding its own local `hub`. Any **non-owning** instance
  that currently has at least one local connection attached to this
  `raceID` subscribes.

This keeps the relay fire-and-forget and asymmetric on purpose:

- **Inbound is fire-and-forget by design, not a shortcut.** `RoomActor.Send`
  already has no return value/reply today (`internal/room/room.go`) — every
  existing in-process caller (`readLoop`) already doesn't wait for an
  event to be applied. Relaying over `room:<raceID>:in` preserves that
  exact contract; nothing that works today starts needing a reply.
- **Outbound reuses the room's own broadcast bytes unchanged.** The owning
  instance already marshals `race_state`/etc. once per tick
  (`broadcastSnapshot`, `internal/room/room.go`); publishing those same
  bytes to Redis is a second fan-out target, not a second encode.

### A local instance always prefers local

Every one of the three call sites above (and the outbound broadcast) first
tries `Registry.Get`. Only on a miss does it fall back to the relay. This
means the overwhelming common case — a single-instance dev environment, or
a multi-instance one where a client happens to land on the owning instance
— pays zero relay overhead and is byte-for-byte the same code path as
today. The relay only activates for the genuinely cross-instance case.

### The one synchronous exception: `IsEvicted`

`ServeHTTP` needs to know *before accepting the WebSocket upgrade* whether
this `userID` was already evicted from this room (`reconnection/grace-
period.md`) — a true request/reply need, unlike the fire-and-forget cases
above. Building a Redis pub/sub request/reply pattern (subscribe to a
correlation-id-scoped reply channel, publish the request, race a timeout)
just for one boolean is disproportionate machinery for what it answers.

Instead: mirror the *eviction state itself* into Redis, readable directly
by any instance with no round trip to the owner:

- `RoomActor`'s existing `evicted map[string]struct{}` (`internal/room/
  room.go`) gains one line at the point a user is added to it: also `SADD
  room:<raceID>:evicted <userID>` (with the room's own TTL/cleanup —
  `Registry.cleanupWhenDone` issues `DEL room:<raceID>:evicted` alongside
  the existing `Release` call from `redis-room-registry.md`).
- `ServeHTTP`'s eviction check becomes: if local, call `actor.IsEvicted`
  exactly as today (no Redis round trip needed when the room is local —
  keep the fast path fast); if not local, `SISMEMBER room:<raceID>:evicted
  <userID>` directly.
- This needs a small new interface in `internal/room`
  (`EvictionMirror`, structural, same shape as `RoomLocator`) so
  `RoomActor` can write to Redis without importing it directly — satisfied
  by (likely) the same `internal/roomlocator.Locator` extended with one
  more method, rather than a fourth Redis-touching package. Confirm at
  `start` whether reusing `Locator` for this or introducing a distinct
  type reads more clearly — they're both "mirror a small piece of
  RoomActor state into Redis for cross-instance reads," so one type
  covering both is the leaning.

### Where the relay code lives

New package `internal/roomrelay` (depends on `internal/room` for
`RoomEvent`/decoding, `internal/roomlocator` for `Owner` lookups and a
Redis pub/sub client — never the reverse; `internal/room` still imports
neither `redis` nor `internal/roomrelay`, preserving the "no HTTP/Redis
imports" property `room-actor-core.md` established for a different
dependency and this spec extends to Redis too).

Sketched surface (finalize exact method names at `start`):

```go
// internal/roomrelay/relay.go
type Relay struct { /* redis client, *room.Registry, *roomlocator.Locator */ }

// Dispatch delivers ev to raceID's room, locally if this instance owns it,
// otherwise via room:<raceID>:in. Used by internal/ws's readLoop and
// internal/race/handler.go's Start (wrapping MarkActive as a RoomEvent —
// see "activated" in internal/room/room.go, already exactly this shape).
func (rl *Relay) Dispatch(ctx context.Context, raceID string, ev room.RoomEvent) error

// SubscribeOut returns a channel delivering raceID's outbound broadcast
// bytes for a non-owning instance, and a cancel func to stop the
// subscription once the last local connection for this raceID detaches.
// Mirrors RoomActor.Broadcast()'s shape so internal/ws's hubRegistry can
// treat a local actor and a remote relay subscription identically — see
// "hub-side integration" below.
func (rl *Relay) SubscribeOut(ctx context.Context, raceID string) (<-chan []byte, func())
```

### Hub-side integration (the outbound path)

`internal/ws/hub.go`'s `hubRegistry.getOrCreate(raceID, actor
*room.RoomActor)` currently requires a real local `*room.RoomActor`. This
needs a second construction path for the non-owning case — **open
question for `start`**: either (a) widen `getOrCreate` to accept a plain
`(<-chan []byte, <-chan struct{})` pair instead of a concrete `*RoomActor`
(the actor case just becomes `actor.Broadcast(), actor.Context().Done()`
at the call site, the relay case becomes `relay.SubscribeOut(...)`'s
return values) — smallest change, keeps `hub` itself completely unaware
Redis exists; or (b) a small `broadcastSource` interface. Leaning toward
(a): `hub`'s own code already only ever uses those two channel-shaped
values, never anything else off `*RoomActor`, so the interface (a) implies
is trivial and doesn't need a name.

`WSHandler.ServeHTTP`'s `registry.Get` miss branch becomes: check
`locator.Owner(ctx, raceID)`; if a live owner is found (even if it's some
other, unreachable-by-name instance — remember, no instance ever needs to
know another's network address, everything flows through Redis), proceed
into `serveConn` using the relay-backed hub source instead of a local
actor; if `Owner` also misses (room genuinely doesn't exist, or its Redis
key expired), `404` exactly as today.

### `readLoop`'s relay path

`readLoop` (`internal/ws/endpoint.go`) currently calls `actor.Send(ev)`
directly. For a relay-backed connection there is no local `*room.RoomActor`
to call — `readLoop` needs a small seam (a `func(room.RoomEvent)` passed
in, satisfied by either `actor.Send` directly (local case) or
`relay.Dispatch(ctx, raceID, ev)` (remote case)) rather than hard-coding
`actor.Send`. Same shape change needed for the `ParticipantLeft` early-
return check currently done via a type-assert on `ev` — that logic doesn't
change, only what receives `ev` afterward does.

### `race/handler.go`'s `Start`

Wraps `MarkActive`'s existing `activated{PromptText: promptText}` event
(`internal/room/room.go`) through the same `Relay.Dispatch` used by
`readLoop` — `Start` doesn't need a bespoke relay path, it needs the same
one WS already uses, called with the `activated` event instead of a
client-decoded one. This is exactly why `Dispatch` takes a `room.RoomEvent`
rather than something WS-message-shaped: `Start` isn't a WebSocket call,
but it produces the same kind of event a WebSocket call would.

## Open questions to resolve at `load`/`start`, not silently assumed

1. **Wire encoding for `room:<raceID>:in`.** `room.RoomEvent` is an
   unexported-method-guarded Go interface with several concrete structs
   (`ParticipantJoined`, `TelemetryReceived`, `ParticipantLeft`,
   `activated`, …) — some already have a natural JSON shape via
   `internal/ws/protocol.go`'s `ClientMessage`/`decodeClientMessage`, but
   `activated` (used by `Start`, never sent over a real WebSocket) does
   not. Decide: extend `internal/ws`'s existing wire format to cover every
   `RoomEvent` variant including `activated`, or define a small
   `internal/roomrelay`-local envelope independent of the WS wire format.
   Leaning toward the latter — coupling the Redis relay's wire format to
   the browser-facing WS protocol means a future WS protocol change could
   silently break inter-instance relay, two concerns that don't need to be
   coupled.
2. **`SubscribeOut` lifecycle.** Subscribing to `room:<raceID>:out` per
   connection would mean N Redis subscriptions for N connections on the
   same non-owning instance to the same room — wasteful and, more
   importantly, N deliveries of every broadcast to that instance instead of
   1. Should be one subscription per `(instance, raceID)` pair, shared
   across every local connection for that race — mirrors `hubRegistry`'s
   existing lazy-create/cleanup-on-empty shape almost exactly (`internal/
   ws/hub.go`'s `getOrCreate`/`onClose`), likely implemented by putting the
   subscribe/unsubscribe call inside the same `hubRegistry.getOrCreate`/
   hub-close path rather than a separate registry — confirm the exact seam
   at `start`.
3. **What if the owning instance dies mid-race (no clean `Release`)?**
   `redis-room-registry.md`'s TTL (60s) is the only recovery: the Redis key
   expires, `Owner` starts returning "not found" for up to 60s even though
   participants are mid-race on connections now silently orphaned (their
   `hub` still exists locally on whatever instance had them, but nothing
   is publishing to `room:<raceID>:out` anymore since the owner is gone).
   This project has no failover story (no other instance takes over a
   dead room's actor and its in-memory `ParticipantState` — that state
   only ever lived in the dead process's RAM and is unrecoverable without
   a much larger redesign, e.g. periodically snapshotting room state to
   Redis, explicitly out of scope). Document this as a known, accepted gap
   — orphaned connections will eventually hit their own read/write errors
   and clean up client-side via the existing reconnect-with-backoff logic
   (`frontend/src/hooks/useRaceSocket.ts`), which will then correctly fail
   after 3 attempts since the room is genuinely gone. Not a silent data
   loss risk beyond what a single-instance process crash already causes
   today — just confirm this reasoning explicitly at `start` rather than
   discovering it only when `multi-instance-dev-setup.md`'s verification
   pass tries to simulate it.
4. **Redis pub/sub delivery is at-most-once, no persistence.** A message
   published while the target instance's subscription hasn't finished
   establishing yet (a real race on connection setup, however narrow) is
   simply lost — matches this project's existing accepted-risk posture for
   other fire-and-forget paths (`finishRace`'s no-retry Postgres write,
   `leave_race`'s fire-and-forget persist), not a new category of risk
   this spec introduces. Worth one explicit regression test simulating a
   message published immediately after subscribe to confirm the ordering
   is actually safe in practice (subscribe-then-confirm before returning,
   using `redis.PubSub.Receive`'s subscription-confirmation message), not
   just assumed safe.

## Concurrency

- Every new goroutine this spec introduces (one `room:<raceID>:in`
  subscriber per owned room, one `room:<raceID>:out` subscriber per
  `(instance, raceID)` with local connections) must be torn down via an
  existing context signal already in scope at the point it's created —
  `actor.Context()` for the inbound subscriber (same lifetime as the
  heartbeat goroutine from `redis-room-registry.md`), the same "last
  connection detached" signal `hubRegistry`'s `onClose` already computes
  for the outbound one. No new bespoke lifecycle primitive should be
  needed anywhere in this spec — if one seems necessary while
  implementing, stop and reconsider the design before adding it.
- `go test -race`, as always, plus this spec specifically needs a real
  two-goroutine (not two-process) test simulating both sides of the relay
  in one test binary — two `Relay` instances sharing one `miniredis` (or
  real local Redis), one publishing, one subscribing, confirming a
  `RoomEvent` round-trips correctly. This is the closest this project can
  get to proving cross-instance correctness without `multi-instance-dev-
  setup.md`'s actual two-process manual verification.

## Notes

- This spec is deliberately scoped to *make cross-instance traffic work*,
  not to make it fast or to eliminate every last edge case (see "Open
  questions" #3/#4 above, both explicitly accepted rather than solved).
  Matches this project's own established precedent of shipping a correct-
  enough-for-a-side-project version and documenting the accepted gap
  (`finishRace`'s no-retry, `leave_race`'s fire-and-forget) rather than
  building production-grade delivery guarantees nothing else in this
  codebase has either.
- `multi-instance-dev-setup.md` is where this spec's design actually gets
  proven against a real second process — treat that spec's verification
  section as this spec's real acceptance test, not just `go test -race` in
  isolation.
