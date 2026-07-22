# Pending Expiry

## Overview

Bounds how long a room can sit `pending` before it's torn down — 5 minutes
from room creation, regardless of how many players are in the lobby. This
exists specifically because of this phase's own design: once pending
players hold live WebSocket connections (`pending-connections.md`), a
lobby the creator never actually starts would otherwise hold those
connections, and the room actor goroutine behind them, open forever. This
is a different condition from `early-spawn.md`'s `noShowTimeout` (empty
room, nobody ever connected, 30s) — this one fires even with a full lobby,
just after a much longer window, and the two share the same underlying
teardown logic rather than duplicating it.

## Requirements

### Timer

- A new `pendingTimeoutDuration = 5 * time.Minute` (package-level `var`,
  not `const`, matching `noShowTimeoutDuration`'s existing pattern so tests
  can shorten it — same reasoning, same file, `internal/room/finish.go`)
- Scheduled once, in `NewRoomActor`, via `time.AfterFunc` — anchored to
  room creation, the same instant `noShowTimeoutDuration`'s timer is
  anchored to (`early-spawn.md` already moved that anchor from `start` to
  creation). Not reset or extended by any activity (a join, a chat, another
  player connecting) — a fixed window, not a rolling idle timeout. Revisit
  only if this feels too aggressive once it's actually used.
- Fires a new `pendingExpired` event through the actor's own `inbox`
  (`Send`), the same self-scheduling pattern `reconnection/grace-period.md`
  already established for `graceTimer`/`ParticipantEvicted` and
  `race-completion/finish-race.md` established for `noShowTimeout` — never
  a direct state mutation from the timer's own goroutine.

### Teardown condition

- `applyEvent`'s `pendingExpired` case: a no-op if `r.status` is already
  `roomActive` (the race started before the timer fired — the common,
  expected outcome) or if the room has already finished/cancelled for any
  other reason.
- If `r.status` is still `roomPending`, tear the room down through the
  **same no-Postgres-write path** `early-spawn.md` already specifies for
  `noShowTimeout`'s now-empty-and-pending case — no real race happened, so
  nothing gets persisted. Factor this into one shared unexported method
  (e.g. `expirePendingRoom()`) that both `noShowTimeout` and `pendingExpired`
  call, rather than two copies of the same teardown logic reached by two
  different timers.
- Broadcasts `race_expired` (`websocket/race-expired-broadcast.md`) to
  every attached connection **before** cancelling the room's context — see
  that spec for why this ordering matters and how it reuses
  `docs/concurrency.md`'s existing drain-before-shutdown guarantee instead
  of reintroducing the bug that guarantee was built to fix.

### Exposing the deadline

The frontend needs a deadline to render a countdown against
(`frontend/live-lobby.md`). Rather than have it hardcode "5 minutes" and
compute `created_at + 5min` itself (duplicating the duration constant in
two places, and vulnerable to client/server clock skew), `GET /races/{id}`
computes and exposes the deadline directly:

- `raceStatusResponse` (`internal/race/dtos.go`) gains
  `PendingExpiresAt *string \`json:"pending_expires_at"\`` — RFC3339,
  matching `CreatedAt`'s existing format. `nil`/omitted once the race is no
  longer `pending` (active, finished, or cancelled) — there's no expiry
  concept once a race has actually started, per this project's existing
  "nil means N/A" convention (`race-completion/finish-race.md`'s
  `FinishTimeMs`).
- Computed as `race.CreatedAt.Add(room.PendingTimeoutDuration)` in
  `RaceHandler.Status` — `internal/race` already imports `internal/room`
  (`RaceHandler` holds `*room.Registry`, since `room-actor/room-registry.md`),
  so this needs `pendingTimeoutDuration` exported from `internal/room` as
  `PendingTimeoutDuration`, not a second constant redefined in
  `internal/race`.

## Data

```go
// internal/room/finish.go
var pendingTimeoutDuration = 5 * time.Minute // exported as PendingTimeoutDuration

type pendingExpired struct{}

func (pendingExpired) isRoomEvent() {}
```

```go
// internal/race/dtos.go — raceStatusResponse gains:
PendingExpiresAt *string `json:"pending_expires_at"`
```

## Notes

- Depends on `room-lifecycle/early-spawn.md` (the shared teardown path this
  reuses) and `room-lifecycle/pending-connections.md` (the `roomPending`/
  `roomActive` status this checks, and the reason live connections exist to
  broadcast to in the first place).
- `websocket/race-expired-broadcast.md` covers the actual message this
  sends before tearing down — this spec only covers the timer and teardown
  decision.
- `go test -race` mandatory, same bar as every other `internal/room` spec
  — in particular, a test proving `pendingExpired` is a genuine no-op once
  status has already flipped to `roomActive` (the timer firing concurrently
  with a real `start` should never tear down a race that's actually
  running).
