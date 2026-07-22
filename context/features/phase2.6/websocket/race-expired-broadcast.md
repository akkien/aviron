# race_expired Broadcast

## Overview

The counterpart to `race-started-broadcast.md`: when a pending room's
5-minute lifetime runs out (`room-lifecycle/pending-expiry.md`) without the
race starting, every attached connection needs to be told why, before the
connection disappears — not left to guess whether it was a network drop.
This is the same principle `docs/concurrency.md`'s finish-race bugfix
already established (a server-initiated close with no explanation is
indistinguishable from a real drop, and the client's reconnect logic will
treat it as one) — applied to a second teardown path, not a new pattern
invented for this one.

## Requirements

### Message

```text
Server -> Client: {"type":"race_expired"}
```

- No payload beyond the type discriminator — there's currently only one way
  a room expires while pending, so a `reason` field would be speculative,
  not something anything actually needs yet.
- Broadcast via the same `hub` fan-out `race_state`/`race_started` already
  use (`internal/ws/hub.go`) — no new delivery mechanism.

### Trigger and ordering

- Sent from the same place `pending-expiry.md`'s shared `expirePendingRoom()`
  teardown method tears the room down — **before** cancelling the room's
  context, exactly like `finishRace` sends `race_finished` before
  cancelling (`internal/room/room.go`).
- This ordering is only safe to rely on because of the fix already
  documented in `docs/concurrency.md`: `hub.run` drains any pending
  broadcast before returning on `done`, and `writeLoop` drains its own
  connection channel off `hub.closed` rather than racing the room's
  context directly. `race_expired` rides that same guarantee — this spec
  adds no new concurrency handling, it depends on what's already proven
  there (including the regression test pattern:
  `TestServeConn_FinishingRaceDeliversFinalStateBeforeClosing` is the
  precedent to follow for a `race_expired`-flavored equivalent, not
  something to re-derive from scratch).

### Client handling

Covered in full by `frontend/live-lobby.md` — in short: distinct from
`evicted` (which specifically means "reconnect grace period lapsed after a
disconnect") and distinct from `leaving` (self-initiated quit). A new state
meaning "this room is gone because nobody started it in time," with its own
message and the same redirect-to-`/races` pattern every other terminal
state already uses.

## Data

```go
type RaceExpiredMessage struct {
	Type string `json:"type"`
}
```

## Notes

- Depends on `room-lifecycle/pending-expiry.md` (there must be a decision
  to expire before there's anything to broadcast) and transitively
  `room-lifecycle/pending-connections.md`/`early-spawn.md`.
- Mirrors `race-started-broadcast.md` deliberately — same message shape
  philosophy (minimal payload, existing fan-out, ordering before teardown)
  applied to the opposite outcome.
