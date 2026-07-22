# Pending Connections

## Overview

With a room actor now existing before a race starts
(`room-lifecycle/early-spawn.md`), this spec covers what changes so a
WebSocket connection can actually attach to it while `pending`: an explicit
pending/active status on `RoomActor`, relaxing `GET /ws`'s rejection rule,
and reconciling three pieces of existing Phase 2 behavior that implicitly
assumed "connected" meant "racing" — telemetry handling, the pending "Leave"
flow, and grace-period/reconnection semantics.

## Requirements

### RoomActor status

`RoomActor` gains an explicit status mirroring `races.status`'s
`pending`/`active` — not `finished`/`cancelled`, which stay handled by the
existing `r.finished` flag plus `cancel()`. Starts `pending` at construction
(`early-spawn.md`), flips to `active` exactly once, from the same place that
broadcasts `race_started` (`websocket/race-started-broadcast.md`).

### Telemetry while pending

`TelemetryReceived` events arriving while status is `pending` are dropped —
same "drop silently" pattern `applyEvent` already uses for stale/duplicate
telemetry (`Seq <= LastSeq`). Defense in depth: the client can't send
telemetry before it has `prompt_text` to type against
(`TypingBox.tsx` doesn't render without it), so this should never actually
trigger in practice, but a malicious or buggy client shouldn't be able to
accumulate progress before the race legitimately starts.

### `GET /ws` while pending

`websocket/ws-endpoint.md`'s existing rejection rule —
"`registry.Get(raceID)` finds no running actor (race is `pending` or
already `finished`)" — changes: reject only when the actor genuinely
doesn't exist (race was never created, or has already finished and been
torn down by the registry's watcher goroutine). A `pending` actor is now a
valid attach target.

`ParticipantJoined`'s existing "broadcast immediately" behavior
(`websocket/ws-endpoint.md`'s requirement 4) already means a pending lobby's
participant list updates live as people join or leave, for free, once this
lands — noted in `phase-2.6-plan.md` as a side benefit, not the goal of this
spec.

### Pending "Leave" reconciliation

Today, `leave-race/leave-race.md`'s pending-race Leave button is REST-only
(`POST /races/{id}/leave`, `RaceScreenSidebar.handleLeavePending`) — built
before any WebSocket connection existed while pending, so REST was the only
option. Now that one does exist, leaving pending should go through the same
`leave_race` WebSocket message / `ParticipantLeft` event the active-race
Quit flow already uses, so both flows share one code path instead of two
independent ones.

**Open question for `load`:** does `POST /races/{id}/leave` stay as a REST
endpoint at all? Leaning toward: yes, unchanged, as a fallback for a client
that isn't WebSocket-connected for some reason (e.g. the connection hasn't
finished its handshake yet) — no breaking change to `internal/race`. The
frontend simply *prefers* sending `leave_race` over the now-open socket when
one exists, matching the active-race pattern, and the room actor's existing
`ParticipantLeft` handling (`departParticipant`, already built in
`leave-race.md`) needs no changes at all.

### Grace period while pending

A pending player's connection dropping (wifi blip, laptop sleep) reuses the
existing 30-second grace-period/eviction mechanism
(`reconnection/grace-period.md`) completely unchanged — nothing in
`ParticipantDisconnected`/`ParticipantEvicted`'s handling assumes the race
is already active. `DisconnectedCount` incrementing during a pending
disconnect is harmless (same field, same meaning, just recorded slightly
earlier in the race's overall lifecycle than it used to be possible to
happen).

## Concurrency

- No new concurrency primitives. The pending/active status is just another
  field mutated exclusively inside `applyEvent`, through the same
  single-writer `inbox` pattern every other piece of `RoomActor` state
  already uses.
- `go test -race` mandatory, same bar as every other `internal/room`/
  `internal/ws` spec in this project.

## Data

```go
type roomStatus int

const (
	roomPending roomStatus = iota
	roomActive
)
```

## Notes

- Depends on `room-lifecycle/early-spawn.md` — the room must exist
  pre-start for any of this to matter.
- `websocket/race-started-broadcast.md` is what actually flips
  `roomActive` and tells connected clients about it — this spec only covers
  the connection/state plumbing that makes that transition possible, not
  the transition itself.
