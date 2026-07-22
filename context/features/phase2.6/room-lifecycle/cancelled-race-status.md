# Cancelled Race Status

## Overview

Discovered while investigating what a user actually sees when they try to
enter a race that stopped before it finished: `pending-expiry.md`'s
`expirePendingRoom()` (shared by both `pendingExpired` and `noShowTimeout`)
broadcasts `race_expired` and tears the room actor down, but never writes to
Postgres — deliberately, per its own comment, since "there's no real race to
persist." That reasoning is correct for `race_participants`/`leaderboard_alltime`
(nobody raced, nothing to score), but incomplete for `races.status` itself,
which is left on `'pending'` forever. Two concrete, confirmed consequences:

- `POST /races/{id}/join` on a room whose actor is already gone **still
  succeeds** — `JoinRace` only checks the Postgres row's status, never the
  registry, and Postgres still says `'pending'`. The joiner gets a real
  session token for a race that no longer exists, then silently fails to
  connect (`GET /ws` 404s, since the actor's gone).
- `GET /races/{id}` reports `status: "pending"` indefinitely, with a
  `pending_expires_at` that's already in the past — nothing distinguishes a
  genuinely still-forming lobby from a dead one.
- On the frontend, `RaceScreenSidebar.tsx` has **already anticipated this
  status value in a comment** ("`'active'`/`'finished'`/`'cancelled'` all
  fall through to the state tree below") without anything ever producing
  it — a fresh visitor to such a race falls through every existing branch
  (`leaving`/`evicted`/`finished` are all false; `promptText` stays `null`
  forever since `RaceScreen.tsx`'s `isActive` gate never fires for a
  non-`"active"` status) and lands on the generic fallback:
  `{connectionError ?? "Loading prompt..."}` — a permanent, misleading
  "Loading prompt..." for a race that's actually dead.

This spec closes the gap the same way `race-completion/finish-race.md`
already closes it for a genuine finish: persist the terminal status before
tearing the room down, so every existing REST guard and every frontend
render path sees the truth instead of a stale `'pending'` row.

## Requirements

### Persisting the status

- `races.status` already anticipates this value — `context/project-overview.md`
  §3's schema comment has always read
  `-- pending|active|finished|cancelled`, and the `status` column itself is
  plain `TEXT` with no `CHECK` constraint (confirmed in
  `migrations/000001_init_schema.up.sql`) — no migration needed, just the
  first code that ever writes it.
- New `RaceCanceller` interface in `internal/room` (`finish.go`, alongside
  `RaceFinisher`/`RaceLeaver`), same import-cycle reasoning as those two:

  ```go
  type RaceCanceller interface {
      CancelRace(ctx context.Context, raceID string) error
  }
  ```

- `RaceService.CancelRace(ctx, raceID) error` satisfies it structurally
  (mirrors `FinishRace`/`LeaveRace`'s existing thin-delegate shape) —
  `internal/race/repository.go`'s `RaceRepository` gains a matching
  `CancelRace` method, implemented in `internal/postgres/race_repository.go`
  as a single statement (no transaction needed, matching `RemoveParticipant`'s
  precedent, not `FinishRace`'s multi-statement one):

  ```sql
  UPDATE races SET status = 'cancelled', ended_at = now()
  WHERE id = $1 AND status = 'pending'
  ```

  The `AND status = 'pending'` guard is defense-in-depth (the caller is
  already guaranteed `!r.active` when this fires), not load-bearing —
  consistent with this project's existing count-then-insert/status-check
  race-condition tolerances elsewhere (`start-race.md`, `join-race.md`).
- `RoomActor` gains a `canceller RaceCanceller` field; `NewRoomActor`/
  `Registry.Spawn` gain a `canceller` parameter alongside the existing
  `finisher`/`leaver`. `RaceHandler.Create`'s `registry.Spawn(...)` call
  passes `h.svc` for all three roles (same concrete value, three structural
  interfaces — same pattern `leaver` already established).

### Wiring into `expirePendingRoom`

- `expirePendingRoom()`'s order changes to mirror `finishRace`'s own
  persist-before-notify-before-teardown shape exactly (`room.go`): call
  `canceller.CancelRace` **first**; on failure, log and return **without**
  broadcasting `race_expired` or tearing down — the room stays running
  rather than silently vanishing on a Postgres hiccup, the same no-retry
  gap `finishRace` already accepts, not a new one. Only once the write
  succeeds does it broadcast `race_expired`, set `r.finished = true`, and
  call `r.cancel()`.
- No branching on *why* `expirePendingRoom` was called (`pendingExpired` vs
  `noShowTimeout` vs a pending room emptied out via `ParticipantLeft`) —
  every path that reaches it persists the same `'cancelled'` status, the
  same way `race_expired`'s own message carries no `reason` field. All
  three paths funnel through this one method today, confirmed by reading
  `checkRaceFinished`'s `!r.active` branch and the `pendingExpired` case in
  `applyEvent` directly — this closes the gap uniformly, not path-by-path.

### Existing REST guards already reject a cancelled race — no new checks needed

Confirmed by reading both directly, not assumed: `JoinRace` and `StartRace`
each only check `r.Status != "pending"` (`ErrRaceNotPending` otherwise) —
neither has any special-case logic for `'active'`/`'finished'` today, so
once a row is genuinely `'cancelled'`, both already reject it for free,
with the exact same `409 race_not_pending` response a `'finished'`/`'active'`
race already gets. Zero handler/service changes needed beyond the write
itself landing.

### Frontend: a real terminal state instead of a fake loading spinner

`RaceScreenSidebar.tsx` gains a `raceDetail.status === "cancelled"` branch,
checked alongside `leaving`/`evicted`/`finished` (before the `promptText`
fallback that currently swallows this case) — same shape as the existing
`evicted` branch (a static message, no auto-navigate, matching that
precedent exactly rather than introducing a new UI affordance nothing else
here has):

```text
This race was cancelled — it wasn't started in time.
```

`RaceScreen.tsx`/`RaceTrack.tsx` need no changes: `isActive` already gates
correctly on `status === "active"`, so a `"cancelled"` race already skips
the WS connection attempt and the prompt-text fetch — the only gap was the
sidebar's fallback message, not the gating logic itself.

## Data

```go
// internal/room/finish.go
type RaceCanceller interface {
    CancelRace(ctx context.Context, raceID string) error
}
```

## Notes

- Depends on `room-lifecycle/pending-expiry.md` (the `expirePendingRoom`
  method this wires into) and transitively `early-spawn.md`/
  `pending-connections.md`.
- Not part of `frontend/live-lobby.md`'s scope, despite touching the same
  sidebar component: `live-lobby.md` is about a client that's *already
  connected* when a room expires (consuming the live `race_expired`
  broadcast, rendering the countdown); this spec is about a client that
  *never connected*, visiting a since-cancelled race cold via plain REST.
  The two are independent — this spec's frontend change has no dependency
  on `live-lobby.md`'s WS-handling work.
- Out of scope: distinguishing *why* a race was cancelled (no-show vs.
  pending-expiry vs. everyone left) anywhere in the UI or schema — matches
  `race-expired-broadcast.md`'s own "no reason field, speculative" call.
- Verification should include: a repository-level test that `CancelRace` is
  a no-op (returns without error, doesn't clobber) when called against a
  race that's already `'active'`/`'finished'` (the `AND status = 'pending'`
  guard), and a room-level test that a `CancelRace` failure leaves the room
  actor running (`r.finished` stays `false`, `r.ctx` stays open, no
  `race_expired` broadcast) rather than tearing down anyway.
