# Room Service Adapter (`race-service` side of the bus)

## Overview

The `race-service`-side half of `room-message-bus.md`'s contract:
replace `internal/ws`'s connection-handling code (which no longer has
any connections to handle — `race-service` never accepts `GET /ws`
directly anymore) with a much smaller pair of goroutines per room that
speak `internal/roomrelay` instead. **`internal/room.RoomActor`'s own
event-application logic — `applyEvent`, the single-writer `inbox`/
`select` loop, all of it — does not change at all.** This spec is
entirely about what feeds `inbox` and what drains `broadcast`, not about
what happens inside `Run()`.

## Current state (confirmed by reading the code)

- `internal/room/room.go`'s `RoomActor` has `inbox chan RoomEvent` (fed,
  today, by `internal/ws/endpoint.go`'s `readLoop` calling `actor.Send`
  once per decoded client frame or detected disconnect) and
  `broadcast chan []byte` (drained, today, by `internal/ws/hub.go`'s
  `hub.run`, which fans each message out to every locally-registered
  per-connection channel).
- `internal/ws/protocol.go` already anticipated exactly the separation
  this spec needs, in its own package doc comment: *"Kept independent of
  connection plumbing... mirroring how `internal/race` keeps `dtos.go`
  separate from `handler.go`."* `ClientMessage`, `decodeClientMessage`,
  and `(ClientMessage) toRoomEvent(userID, displayName)` are pure wire-
  protocol logic with no dependency on an actual `*websocket.Conn` —
  this spec reuses all three completely unchanged, just called from a
  bus-message handler instead of a WS reader goroutine.
- `internal/ws/hub.go` and `internal/ws/endpoint.go`'s connection-
  handling code (`WSHandler`, `serveConn`, `readLoop`, `writeLoop`,
  `session_token.go`'s `verifySessionToken`) have no remaining caller on
  the `race-service` side once this ships — `race-service` never
  terminates a WebSocket again. These move to `ws-gateway.md`'s package
  (`internal/wsgateway`), adapted for a bus-fed input/output instead of a
  directly-owned `RoomActor`, not left behind as dead code in
  `internal/ws`.

## Requirements

### Feeding `inbox`: one bus-subscriber goroutine per room

Replaces N per-connection `readLoop` goroutines (one per WS connection,
today) with exactly one goroutine per room, since every client's frames
for a given race are already multiplexed onto the single NATS subject
`room.{race_id}.in` (`room-message-bus.md`) by the time they reach here:

```go
// internal/room/registry.go — Registry.Spawn, alongside the existing
// Locator.Claim call and go actor.Run()
sub, err := relay.SubscribeIn(ctx, raceID)
// ...
go func() {
    for env := range sub {
        switch env.Kind {
        case roomrelay.InboundKindMessage:
            msg, err := ws.DecodeClientMessage(env.Message) // exported for this caller
            if err != nil {
                logger.Warn("room-service-adapter: decode failed", ...)
                continue // malformed frame — log and drop, exactly like readLoop does today
            }
            ev, err := msg.ToRoomEvent(env.UserID, env.DisplayName)
            if err != nil {
                continue
            }
            actor.Send(ev)
        case roomrelay.InboundKindDisconnected:
            actor.Send(room.ParticipantDisconnected{UserID: env.UserID})
        }
    }
}()
```

- `ws.decodeClientMessage`/`ClientMessage.toRoomEvent` need exporting
  (`DecodeClientMessage`/`ToRoomEvent`) since their caller is now outside
  `internal/ws` — the only signature-level change either function needs;
  their bodies are untouched.
- This goroutine's lifetime matches the room's: started right after
  `Locator.Claim` succeeds (same place `RoomActor.Run()` already starts),
  torn down when the room's context ends — `sub`'s channel closes when
  `ctx` is cancelled (mirrors how `roomlocator.SubscribeRoomEvents`
  already behaves), so a plain `range` over it exits cleanly with no
  separate `select`-on-`ctx.Done()` needed in this loop.
- Malformed-frame handling matches `readLoop`'s existing behavior exactly:
  log and drop, never treat it as fatal to the room or the connection —
  a hostile or buggy client shouldn't be able to kill a room for everyone
  else in it.

### Draining `broadcast`: one bus-publisher goroutine per room

Replaces `internal/ws.hub`'s local N-way fan-out with a single publish
per message — there's no local fan-out left to do, `race-service` has
zero direct connections:

```go
go func() {
    for msg := range actor.Broadcast() {
        relay.PublishOut(ctx, raceID, roomrelay.OutboundEnvelope{
            Kind:    roomrelay.OutboundKindBroadcast,
            RaceID:  raceID,
            Payload: msg,
        })
    }
    // actor.Broadcast()'s channel closing is this room's signal to
    // finish — see "Ordering: room_closed must follow every broadcast"
    // below before publishing it.
    relay.PublishOut(context.Background(), raceID, roomrelay.OutboundEnvelope{
        Kind:   roomrelay.OutboundKindRoomClosed,
        RaceID: raceID,
    })
}()
```

### Ordering: `room_closed` must follow every broadcast, not race it

`internal/ws/hub.go`'s own `done` case has a detailed comment explaining
exactly this hazard for the in-process version: `broadcast` (still
carrying the final `race_state`/`race_finished`) and `done` (just
cancelled) become ready at essentially the same moment, and Go's `select`
picks a ready case pseudo-randomly, not in send order — without
deliberately draining `broadcast` first, `hub.run` could observe `done`
before finishing forwarding what's already queued, and a client would see
its connection die with no `race_finished`, indistinguishable from a raw
drop. **The bus version has the identical hazard, worse:** two different
processes now, so there's no shared memory to reason about at all, only
message order on two different channels a `ws-gateway` subscriber has no
inherent way to correlate. Resolution: the code sketch above relies on
`actor.Broadcast()`'s channel itself closing only after every pending
message has already been sent on it (confirm this is how the room actor's
own shutdown sequence is ordered at `start` — if `Broadcast()`'s channel
can close while messages are still logically pending elsewhere, this
goroutine needs an explicit drain loop instead, the same shape
`hub.run`'s `done` case already uses) — a plain `for msg := range
actor.Broadcast()` only proceeds to publish `room_closed` after the range
loop itself has exhausted every already-sent message, which is exactly
the ordering guarantee needed **only if** the channel's own close
happens after its last send, not concurrently with it.

### `ws-gateway`'s corresponding obligation

Not this spec's own requirement (see `ws-gateway.md`), but worth stating
here since it's the other half of the same correctness property: a
`ws-gateway` subscriber must fully drain any already-delivered
`OutboundKindBroadcast` messages to its local connections *before*
acting on a subsequently-received `OutboundKindRoomClosed` — mirroring
`writeLoop`'s existing `hubClosed` drain-then-return logic exactly, just
crossing a bus instead of a local channel now.

## Data

```go
// internal/room/registry.go — new fields/params on whatever already
// wires the Locator in (redis-room-registry.md)
type Registry struct {
    // ...existing fields...
    relay *roomrelay.Relay // new — internal/roomrelay's client (NATS — room-message-bus.md)
}
```

`Registry.Spawn` gains the two goroutines above, started right after
`Locator.Claim` and `NewRoomActor`/`go actor.Run()` — no change to
`RoomActor`'s own constructor signature, since neither goroutine lives
inside `RoomActor` itself (consistent with the existing pattern of
`Registry` owning per-room lifecycle concerns like the heartbeat refresh,
not `RoomActor` reaching out to Redis itself).

## Concurrency

- Both new goroutines are per-room, same lifetime as `RoomActor.Run()`
  itself — no new goroutine-leak surface beyond what `redis-room-
  registry.md`'s existing heartbeat goroutine already establishes a
  pattern for (tied to the room's own `ctx`, exits when it's cancelled).
- The inbox-feeding goroutine calls `actor.Send(ev)`, the exact same
  single entry point external callers already use today — no new
  synchronization primitive needed, `RoomActor`'s single-writer principle
  is completely unaffected by *how many* goroutines call `Send`, only by
  ensuring nothing calls `applyEvent` directly, which nothing here does.
- `go test -race` coverage: the inbox-feeding goroutine against a fake
  `roomrelay` publishing a sequence of `InboundEnvelope`s, asserting the
  right `RoomEvent` sequence reaches a real `RoomActor`'s `applyEvent`
  (via its normal test harness); the broadcast-draining goroutine against
  a room actor whose `Broadcast()` channel is closed mid-test, asserting
  `room_closed` is only ever published after every prior message.

## Testing

- Unit: `ws.DecodeClientMessage`/`ToRoomEvent`'s existing test coverage
  is untouched (same functions, just exported) — no new tests needed for
  the conversion logic itself, only for the new goroutines wrapping it.
- Integration: a real `internal/roomrelay` against a real (or embedded
  test server, per `room-message-bus.md`'s Notes) NATS instance,
  publishing a realistic sequence (`join_race`, several `telemetry`,
  `leave_race`) on `room.{race_id}.in` and asserting the correct final
  `RoomActor` state — this is the test that actually proves
  `room-message-bus.md`'s envelope format round-trips correctly on this
  side, not just that the Go code compiles against it.
- `multi-instance-dev-setup.md` is the real end-to-end proof, run against
  both this spec and `ws-gateway.md` together.

## Notes

- `internal/ws/hub.go` and its test file are deleted as part of this
  spec landing (or moved wholesale into `internal/wsgateway`, adapted —
  confirm at `start` whether starting from a copy is faster than
  rewriting from this spec's description; either produces the same
  design). `internal/ws/endpoint.go`'s `WSHandler`/`serveConn`/
  `readLoop`/`writeLoop` and `session_token.go` move to `ws-gateway.md`'s
  package in full — see that spec for their adapted shape.
- `internal/ws/protocol.go` stays in `internal/ws`, now imported by both
  `race-service` (this spec) and `ws-gateway` (which still needs
  `decodeClientMessage` to validate a frame is well-formed JSON before
  ever publishing it onto the bus, and `verifySessionToken` for
  connection-time auth) — `internal/ws` becomes a shared wire-protocol
  package, not a `race-service`-only one, the same relationship
  `internal/roomlocator` already has with both `race-service` and (in the
  removed `race-router` design) the old router binary.
