# race_started Broadcast

## Overview

The actual fix for the fairness problem this phase exists to solve: a new
server → client message, `race_started`, broadcast to every connection
already attached to a room the instant `POST /races/{id}/start` succeeds —
reaching every pending lobby member simultaneously (network latency only),
instead of each client discovering the transition independently through a
REST poll or a manual "Refresh" click.

## Requirements

### Message

```text
Server -> Client: {"type":"race_started","prompt_text":"..."}
```

- Carries `prompt_text` directly, so a client doesn't need a separate
  `GET /races/{id}/text` round-trip before it can start typing — removes
  one more source of per-client delay variance between when different
  players actually get to begin.
- Broadcast via the exact same `hub`/fan-out mechanism `race_state` already
  uses (`internal/ws/hub.go`) — not a new delivery path, just a new message
  type flowing through the existing one. No changes needed to `hub.go`
  itself.

### Trigger

`RaceHandler.Start` (after `RaceService.StartRace` succeeds: prompt
generated, `races.status` flipped to `active` in Postgres) tells the room
actor to transition to `roomActive`
(`room-lifecycle/pending-connections.md`) and broadcast `race_started`.

**Open question for `load`:** a direct method call on the actor (e.g.
`actor.Start(promptText, distanceMeters)`), or a new `RoomEvent` variant
sent through the existing `Send`/`inbox` path? Leaning toward
event-through-inbox — every other piece of room-state mutation already goes
through that path (`room-actor-core.md`'s single-writer principle), and a
direct method call bypassing `inbox` would be the first exception to it.

Must broadcast to every **already-connected** pending client, not just
future ones — that's the entire point of this spec. The regression test
here should specifically: spawn a room, attach 2+ fake connections while
pending (mirroring `internal/ws/endpoint_test.go`'s existing patterns),
trigger start, and assert every connection receives `race_started` at (as
close to) the same moment — not staggered, not missing for any connection
that was already attached.

### `GET /races/{id}/text` stays

Not removed — still needed for a client that loads the race page (or
reconnects) *after* `start` already happened, exactly as today
(`races/race-status.md`'s existing design). `race_started` is additive, for
clients that were already connected when the transition happened; it is not
a replacement for that fallback path.

## Concurrency

- Broadcasting to N already-registered connections at once is exactly what
  `hub.run`'s existing fan-out loop already does for every `race_state`
  tick — no new concurrency pattern, just a new message type riding through
  it.
- This is the opposite of a teardown path (starting a race, not finishing
  one), so `docs/concurrency.md`'s drain-before-shutdown fix shouldn't be
  relevant here — but worth one sentence of explicit reasoning confirming
  that during `start`, not silently assumed.

## Data

```go
type RaceStartedMessage struct {
	Type       string `json:"type"`
	PromptText string `json:"prompt_text"`
}
```

## Notes

- Depends on `room-lifecycle/pending-connections.md` (there must be pending
  connections to broadcast to) and transitively
  `room-lifecycle/early-spawn.md`.
- `frontend/live-lobby.md` is the consumer of this message — this spec only
  defines the wire shape and the server-side trigger.
