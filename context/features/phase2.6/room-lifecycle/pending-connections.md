# Pending Connections

## Overview

With a room actor now existing before a race starts
(`room-lifecycle/early-spawn.md`), a WebSocket connection can already attach
to it while `pending` — `GET /ws` only ever checked whether the actor
exists, not race status, so that door opened as an unverified side effect
the moment `early-spawn.md` moved `Spawn` to race creation. This spec is
about making that safe: gating what a pending connection is actually
allowed to *do* (telemetry, until the race is genuinely active), adding
test coverage proving the already-open door is safe, and reconciling three
pieces of existing Phase 2 behavior that implicitly assumed "connected"
meant "racing" — telemetry handling, leaving a race, and grace-period/
reconnection semantics.

## Requirements

### RoomActor status

`RoomActor` already has what this needs: `early-spawn.md`'s `active bool`
(`room.go:58`), `false` at construction, flipped `true` exactly once by
`MarkActive()`, which the same place that will broadcast `race_started`
(`websocket/race-started-broadcast.md`) calls. No new field.

### Telemetry while pending

`TelemetryReceived` events arriving while `!r.active` are dropped — same
"drop silently" pattern `applyEvent` already uses for stale/duplicate
telemetry (`Seq <= LastSeq`). Not defense-in-depth against a hypothetical:
since a pending room actor already accepts WebSocket connections (see
above) and `applyEvent`'s `TelemetryReceived` case has no active-gating at
all today, a client that connects to a still-pending race can, right now,
send telemetry and have it accepted — updating `WordsCorrect` and
potentially even triggering a finish before the race has legitimately
started. This closes a gap that's live today, not one being pre-empted.

### `GET /ws` while pending

No code change: `internal/ws/endpoint.go`'s handshake only ever checks
`registry.Get(raceID)` succeeding, never race status directly — so a
`pending` room actor (which now exists from creation) is already a valid
attach target today. Two things this spec actually does about it:

- Adds a short comment at that check clarifying a `pending` actor is an
  intentionally valid target — today's comment doesn't mention room status
  at all, which reads as an oversight to a future reader rather than the
  deliberate behavior it now is.
- Fixes the stale line in `context/features/phase2/websocket/ws-endpoint.md`
  that still says to reject when "the race is `pending` or already
  `finished`" — no longer true since `early-spawn.md`.

`ParticipantJoined`'s existing "broadcast immediately" behavior
(`websocket/ws-endpoint.md`'s requirement 4) already means a pending lobby's
participant list updates live as people join or leave, for free, once this
lands — noted in `phase-2.6-plan.md` as a side benefit, not the goal of this
spec.

### Pending "Leave" unification

**Resolved, not left open**: `POST /races/{id}/leave` is removed entirely.
Leaving a race — pending or active — goes through the WebSocket
`leave_race` message / `ParticipantLeft` event exclusively; the room actor
decides internally what that means, based on its own `active` field, rather
than the client (or a second REST code path) deciding for it.

`internal/ws/protocol.go`'s `leave_race` → `ParticipantLeft` decoding
already doesn't care about race status today — no change needed there. The
new branching lives entirely in `applyEvent`'s `ParticipantLeft` case
(`internal/room/room.go`):

- **`r.active` (today's mid-race quit, unchanged)**: goes through the
  existing `departParticipant` — moved into `departedParticipants`, added
  to `evicted`, counted at finish time with the shared last-place rank
  `leave-race/leave-race.md` specified. Quitting mid-race is a real result
  worth recording, and someone who already gave up shouldn't be able to
  silently reattach.
- **`!r.active` (new)**: removed from `r.participants` directly — no
  `departedParticipants`, no `evicted`. There's no "race result" for
  someone who backed out before the race existed
  (`early-spawn.md`'s same reasoning for why a never-active room doesn't
  persist anything), and unlike a mid-race quitter, someone who leaves a
  lobby should be free to `POST /races/{id}/join` again and rejoin
  cleanly — marking them `evicted` would wrongly block that legitimate
  rejoin. The room actor also issues the real `DELETE FROM
  race_participants` this used to be — a new `RaceLeaver` interface
  (`internal/room`, mirroring `RaceFinisher`'s existing shape exactly):

  ```go
  type RaceLeaver interface {
      LeaveRace(ctx context.Context, raceID, userID string) error
  }
  ```

  `RaceService.LeaveRace(ctx, raceID, userID) error` (`internal/race/service.go:100`)
  already has this exact signature and already does this exact delete —
  it satisfies `RaceLeaver` structurally with zero changes to
  `internal/race`. `NewRoomActor`/`Registry.Spawn` gain a `leaver
  RaceLeaver` parameter alongside the existing `finisher`; `RaceHandler.Create`
  passes `h.svc` for both, the same concrete value in two structural roles.

**Ordering and failure handling**: remove from `r.participants` first,
*then* call `leaver.LeaveRace` — not the other way around. The reader
goroutine that decoded `leave_race` returns immediately after sending the
event (`leave-race.md`'s existing behavior: "closes the connection right
after sending the event, rather than waiting for the client to hang up"),
so unlike a real disconnect, no `ParticipantDisconnected`/grace-period
path will ever run for this participant to clean them up later if we wait.
Removing in-memory state optimistically, before the Postgres call, avoids
a permanently stuck participant if that call fails. A failed
`LeaveRace` call is logged and not retried — the same accepted gap
`finishRace`'s own Postgres write already has, not a new one. This is
called synchronously inside `applyEvent`, blocking the single-writer loop
for the round-trip, exactly like `finishRace` already does — a pending
lobby's write volume (occasional joins/leaves) is nowhere near the 250ms
tick's telemetry-handling hot path, so this doesn't need to be async.

**Known UX gap, accepted not solved here**: REST gave a failed leave an
HTTP error the client could show. `leave_race` is fire-and-forget — a
client that sends it has no way to learn the Postgres delete failed, and
today's frontend pattern (matching the existing active-quit `leaveRace()`)
is to show "left" optimistically regardless. Same tradeoff this project
already accepted for `finishRace`, now extended to this path too — flagging
explicitly since it's a real (if rare) UX regression from REST's
request/response model, not something to discover later.

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

No new status type — `early-spawn.md` already added `active bool` to
`RoomActor`, and it fully covers pending/active with no third state to
express (`finished`/`cancelled` stay handled separately by `r.finished` +
`cancel()`). This spec reuses that field as-is rather than introducing a
parallel `roomStatus` enum.

```go
// internal/room — mirrors RaceFinisher exactly; RaceService satisfies it
// structurally via its existing LeaveRace method, no changes needed there.
type RaceLeaver interface {
    LeaveRace(ctx context.Context, raceID, userID string) error
}
```

## Notes

- Depends on `room-lifecycle/early-spawn.md` — the room must exist
  pre-start for any of this to matter, and `active`/`MarkActive()` are
  where this feature's changes are built.
- `websocket/race-started-broadcast.md` is what actually flips `active` to
  `true` and tells connected clients about it — this spec only covers the
  connection/state plumbing that makes that transition possible (plus the
  now-unified leave path), not the transition itself.
- Removing `POST /races/{id}/leave` touches an already-shipped Phase 2
  spec (`context/features/phase2/leave-race/leave-race.md`) and a later
  Phase 2.6 one (`frontend/live-lobby.md`) — both updated alongside this
  one, not left to drift.
