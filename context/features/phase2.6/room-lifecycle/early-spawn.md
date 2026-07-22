# Early Room Spawn

## Overview

Moves `Registry.Spawn` from `POST /races/{id}/start` (`RaceHandler.Start`,
`internal/race/handler.go:228`) to `POST /races` (`RaceHandler.Create`) — a
room actor now exists for a race's entire lifetime, from creation through
finish, not just from `start` onward. This is the structural prerequisite
for `room-lifecycle/pending-connections.md`: a client can only hold a
WebSocket connection to a room actor that already exists.

## Requirements

### Spawn point

- `RaceHandler.Create` calls `registry.Spawn(...)` immediately after a
  successful `CreateRace`, the same way `RaceHandler.Start` does today —
  same shape, just called one handler earlier.
- `RaceHandler.Start` no longer spawns. It looks up the already-spawned
  actor via `registry.Get(raceID)` and tells it to transition to active
  (`pending-connections.md` covers the actor's new pending/active state;
  `websocket/race-started-broadcast.md` covers what that transition
  broadcasts). If `Get` finds nothing — the room actor already tore itself
  down somehow before `start` was ever called — that's a real error
  (`500`, logged), not a silent no-op: it means room lifecycle state has
  drifted from race lifecycle state somewhere, which should never happen
  under this spec's design.

### promptText at construction time

`NewRoomActor`'s current signature is
`NewRoomActor(ctx context.Context, id, promptText string, distanceMeters int, broadcast chan []byte, finisher RaceFinisher) *RoomActor`.
At `POST /races` time, `promptText` doesn't exist yet — it's only generated
by `POST /races/{id}/start` (`internal/race/prompt.go`'s
`generatePromptText`, called from `RaceService.StartRace`). `distanceMeters`
*is* known at create time (`CreateRaceRequest.DistanceMeters`).

**Open design question, to resolve during `load`/`start`:** does
`NewRoomActor` keep a `promptText` field and gain a way to set it later
(e.g. a `SetPromptText`-style event through the actor's own `inbox`, kept
consistent with `room-actor-core.md`'s single-writer principle), or does the
actor stop holding `promptText` as its own state entirely — since it never
inspects race text server-side (`project-overview.md` §13's
never-verify-typed-text trust model), it may not need to hold the string at
all, only ever need to hand it off once, to `RaceHandler.Start` for the
`race_started` broadcast in `websocket/race-started-broadcast.md`. Leaning
toward the latter: drop `promptText` from `RoomActor`'s state and
`NewRoomActor`'s signature, since keeping data an actor never reads is
exactly the kind of unused field this project's coding standards guard
against.

### Reconciling `noShowTimeout` (finish-race.md)

Today's `noShowTimeout` (`internal/room/finish.go`, a room-level
`time.AfterFunc` that fires if nobody ever connects) calls
`checkRaceFinished()`, which is meaningful once a race has real
participants/results to score — an assumption that held because a room only
ever existed *after* a race was already active. Now that a room exists from
creation:

- A room that's still genuinely `pending` when `noShowTimeout` fires (nobody
  ever connected at all — reachable even though `CreateRace` auto-joins the
  creator, if they immediately close the tab before any WebSocket attaches)
  must **not** call `finisher.FinishRace` — there's no race to record: no
  real `started_at`, no participant who actually raced. It should just
  cancel the room and let the registry's existing watcher goroutine clean it
  up, with zero Postgres writes.
- A room that reaches zero live participants *after* having gone active
  keeps today's existing finish-and-persist behavior unchanged.
- `checkRaceFinished`'s "zero live participants" condition needs to
  distinguish these two cases — gated on the pending/active status
  `pending-connections.md` adds to `RoomActor`.

`noShowTimeoutDuration` (currently 30s, `internal/room/finish.go`) — keep as
the same single timer, now measured from room creation instead of from
`start`. No new timer is needed; a race that's been sitting `pending` for 30
seconds with nobody connected is exactly the case this timeout already
existed to catch, just reached from a slightly different starting point.

This is the narrower of two related teardown conditions —
`room-lifecycle/pending-expiry.md` adds a second, longer (5-minute) timer
that fires regardless of occupancy, not just when the room is empty. Both
should share the same no-Postgres-write teardown logic described above
(factored into one method, per that spec), reached by two different timers
for two different reasons: an empty room is pure waste and deserves fast
cleanup, while a room with real people in it deserves more patience before
being torn down.

## Data

```go
// registry.go — promptText param removed per the open question above;
// resolve during load/start before finalizing this signature.
func (reg *Registry) Spawn(ctx context.Context, raceID string, distanceMeters int, finisher RaceFinisher) *RoomActor
```

## Notes

- `RaceHandler` already holds both `*room.Registry` and the process's root
  `context.Context` (wired since `room-actor/room-registry.md`) — no new
  dependency threading, just moving which handler calls `Spawn`.
- Depends on nothing else in Phase 2.6 — this is the foundational spec the
  other three build on.
- `go test -race` mandatory, consistent with every other
  `internal/room`/`internal/ws` spec in this project.
