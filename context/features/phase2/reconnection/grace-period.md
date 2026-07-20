# Reconnection & Grace Period

## Overview

The JD's single most emphasized real-time skill: keeping client state in sync and handling reconnection. context/project-overview.md §4.3 — when a client's WebSocket connection drops abruptly (home wifi, mobile app backgrounded, laptop lid closed), the room actor doesn't treat that as "this player quit." It marks them disconnected, keeps their state in the room for a grace period, and reattaches them cleanly if they come back in time. Depends on `websocket/ws-endpoint.md` already working — there has to be a connection before there can be a disconnection.

## Requirements

### On Abrupt Close

- When a connection's reader goroutine exits because the underlying WebSocket closed (not because the room is shutting down), it sends a `ParticipantDisconnected` event to the room actor's `inbox` rather than the actor learning about it any other way — consistent with `room-actor-core.md`'s single-writer principle (nothing sets `DisconnectedAt` except the actor's own `applyEvent`)
- `applyEvent` sets that participant's `DisconnectedAt = time.Now()` — the participant stays in `participants`, still included in `race_state` broadcasts (so other players see they're still "in" the race, just not actively typing), for up to N seconds (grace period; 30s per §4.3's suggestion)

### Reattachment

- A new `ParticipantReconnected` event variant (extending `room-actor-core.md`'s `RoomEvent` sum type) is applied when `websocket/ws-endpoint.md` successfully verifies a reconnecting client's `session_token` for a `user_id` that's currently disconnected-but-within-grace-period in that room
- On reattachment: clear `DisconnectedAt` (back to `nil`), attach the new connection's writer channel, and immediately send that one client a full `race_state` snapshot (not just wait for the next tick) — so their UI resyncs instantly instead of showing stale state for up to 250ms
- A `session_token` presented for a `user_id` that's past its grace period (already removed, see below) is rejected the same way `websocket/ws-endpoint.md` rejects any other invalid attach attempt — from the client's perspective this looks identical to "the race doesn't have you as a participant," which is accurate by then

### Grace Period Expiry

- Each disconnect starts (or restarts, if they'd reconnected and dropped again) a per-participant timer. If it fires before a reconnect happens, the room actor applies a `ParticipantLeft` event: remove them from `participants` for good, and the next broadcast reflects their absence to everyone else
- Implemented as a event the room actor schedules for itself (e.g., `time.AfterFunc` sending a `RoomEvent` back onto its own `inbox`) rather than a second goroutine reaching into `participants` — keeps the single-writer principle intact; the timer only ever produces an event, it never touches state directly

## Concurrency

- The grace-period timer must be cancelable: if a participant reconnects before it fires, the pending timer for their earlier disconnect has to be stopped, or a stale expiry could incorrectly remove a participant who's since reattached. Track the active `*time.Timer` per disconnected participant (e.g., on `ParticipantState` itself) so reattachment can call `Stop()` on it.
- Dedicated tests here per context/project-overview.md §10 and §4.3: simulate a mid-race disconnect, reconnect within the grace period (assert state is preserved, no duplicate participant), and separately simulate one that expires (assert the participant is removed and others are notified) — both under `go test -race`.

## Data

```go
// Added to room-actor-core.md's RoomEvent sum type:
type ParticipantReconnected struct {
    UserID string
    Writer chan<- []byte // the new connection's outbound channel
}
type ParticipantLeft struct {
    UserID string
}
```

- No new Postgres columns needed for the grace period itself — `race_participants.disconnected_count` (already in the schema, context/project-overview.md §3) is incremented once per disconnect event, persisted as part of whatever batched write already exists rather than a write per disconnect (consistent with `workout_samples`' existing batching rationale).

## Notes

- This spec only covers the grace period *while a race is active*. What happens to `disconnected_count` and final results when a race ends is `race-completion/finish-race.md`'s concern.
- Grace period length (30s suggested in §4.3) should be a named constant, not hardcoded inline — easy to tune later without hunting for magic numbers, but not worth making it configurable per-race until there's an actual reason to.
