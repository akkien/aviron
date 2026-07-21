# Reconnection & Grace Period

## Overview

The JD's single most emphasized real-time skill: keeping client state in sync and handling reconnection. context/project-overview.md §4.3 — when a client's WebSocket connection drops abruptly (home wifi, mobile app backgrounded, laptop lid closed), the room actor doesn't treat that as "this player quit." It marks them disconnected, keeps their state in the room for a grace period, and reattaches them cleanly if they come back in time. Depends on `websocket/ws-endpoint.md` already working — there has to be a connection before there can be a disconnection.

## Requirements

### On Abrupt Close

- When a connection's reader goroutine exits because the underlying WebSocket closed (not because the room is shutting down), it sends a `ParticipantDisconnected` event to the room actor's `inbox` rather than the actor learning about it any other way — consistent with `room-actor-core.md`'s single-writer principle (nothing sets `DisconnectedAt` except the actor's own `applyEvent`)
- `applyEvent` sets that participant's `DisconnectedAt = time.Now()` — the participant stays in `participants`, still included in `race_state` broadcasts (so other players see they're still "in" the race, just not actively typing), for up to N seconds (grace period; 30s per §4.3's suggestion)

### Reattachment

- **Decided: no distinct `ParticipantReconnected` event.** A reconnecting client sends the same `join_race` message any fresh join does, which `websocket/protocol.md` already decodes into the same `ParticipantJoined{UserID, DisplayName}` event and hands to `actor.Send` — no `internal/ws` changes needed. `applyEvent`'s existing `ParticipantJoined` case is what tells a reconnect apart from a fresh join: if `r.participants[UserID]` already exists **and** `DisconnectedAt != nil`, treat it as a reconnect — stop the pending grace timer, clear `DisconnectedAt`, keep `WordsCorrect`/`LastSeq` — instead of allocating a fresh `ParticipantState`. This also means "immediately send a full snapshot" below falls out for free: `applyEvent`'s `ParticipantJoined` handling already broadcasts on every apply, reconnect or not, so the reconnecting client's own new connection (already registered with the room's `hub` by the time this event is applied — see below) gets resynced as a side effect, no one-client-only send needed.
- Per-connection writer channels stay entirely `internal/ws`'s concern (the `hub`, from `websocket/ws-endpoint.md`) — a reconnecting client's new connection registers its own outbound channel with the room's `hub` through the exact same code path any first-time connection does. `RoomActor` never holds or needs a writer channel itself.
- **Decided: a `session_token` for a `user_id` past its grace period is actually rejected**, not silently treated as a fresh join. `RoomActor` tracks which `user_id`s have been evicted (populated when `ParticipantLeft` fires — see Grace Period Expiry) and exposes a synchronous query, `IsEvicted(userID string) bool`, that `websocket/ws-endpoint.md` calls during the WS handshake — before the upgrade happens — rejecting with the exact same response as an invalid/expired `session_token`. From the client's perspective this looks identical to "the race doesn't have you as a participant," which is accurate by then.

### Grace Period Expiry

- Each disconnect starts (or restarts, if they'd reconnected and dropped again) a per-participant timer. If it fires before a reconnect happens, the room actor applies a `ParticipantLeft` event: remove them from `participants` for good, and the next broadcast reflects their absence to everyone else
- Implemented as a event the room actor schedules for itself (e.g., `time.AfterFunc` sending a `RoomEvent` back onto its own `inbox`) rather than a second goroutine reaching into `participants` — keeps the single-writer principle intact; the timer only ever produces an event, it never touches state directly
- `applyEvent`'s `ParticipantLeft` case also records the `user_id` in the evicted set described above, in addition to deleting them from `participants` — this is the only thing that makes `IsEvicted` return `true` for them, and the only way a later reconnect attempt for that exact `user_id` gets rejected instead of silently rejoining fresh
- **This feature stops at removing the participant from `participants`.** If a `ParticipantLeft` event happens to empty `participants` entirely (the last remaining participant leaves), that is `race-completion/finish-race.md`'s "zero participants remaining active" finish condition firing — not something this feature detects, decides, or acts on itself. This feature does not check whether the room is now empty, does not touch Postgres, does not broadcast `race_finished`, and does not tear down the room actor or remove it from `room.Registry`. All of that is `finish-race.md`'s job, triggered by the same `ParticipantLeft` event this feature produces. See Notes.

## Concurrency

- The grace-period timer must be cancelable: if a participant reconnects before it fires, the pending timer for their earlier disconnect has to be stopped, or a stale expiry could incorrectly remove a participant who's since reattached. Track the active `*time.Timer` per disconnected participant (e.g., on `ParticipantState` itself) so reattachment can call `Stop()` on it.
- `IsEvicted` is the first *query* (not fire-and-forget) `RoomActor` needs to answer from outside its own goroutine — implemented as a request placed on the same `inbox` as any other `RoomEvent`, carrying a reply channel, and answered from inside `applyEvent`'s single-writer loop rather than a second goroutine reading eviction/participant state directly. Same channel-driven pattern this actor already uses everywhere else, just request/response instead of fire-and-forget.
- Dedicated tests here per context/project-overview.md §10 and §4.3: simulate a mid-race disconnect, reconnect within the grace period (assert state is preserved, no duplicate participant), separately simulate one that expires (assert the participant is removed and others are notified), and a fourth case — a reconnect attempt *after* expiry is rejected via `IsEvicted` rather than silently rejoining as a fresh participant. All under `go test -race`.

## Data

```go
// Added to room-actor-core.md's RoomEvent sum type:
type ParticipantLeft struct {
    UserID string
}
```

- `ParticipantReconnected` does **not** exist as a separate event — a reconnect is just another `ParticipantJoined`; `applyEvent` tells it apart from a fresh join by checking existing participant state (see Reattachment above).
- `IsEvicted` is a synchronous method, not a `RoomEvent` callers send-and-forget:

  ```go
  func (r *RoomActor) IsEvicted(userID string) bool
  ```

  Backing it internally: a query placed on the same `inbox` as any other event, answered inside `applyEvent`:

  ```go
  type evictionQuery struct {
      UserID string
      Reply  chan<- bool
  }
  ```

- No new Postgres columns needed, and **this feature makes no Postgres writes at all** — `race_participants.disconnected_count` (already in the schema, context/project-overview.md §3) is tracked purely in memory, as a counter on `ParticipantState` incremented once per `ParticipantDisconnected` event. It's `race-completion/finish-race.md`'s transaction (`ParticipantResult.DisconnectedCount` in that spec's Data section) that persists the final value, once, when the race finishes — not a write per disconnect, and not this feature's concern.

## Notes

- This spec only covers the grace period *while a race is active*, and is deliberately Postgres-free — no DB round trip anywhere in this feature. What happens to `disconnected_count` and final results when a race ends, **including a race every participant abandons**, is entirely `race-completion/finish-race.md`'s concern.
- **Resolving an attribution mismatch with `room-registry.md`**: that spec's Teardown section says a room actor is removed from the registry "when every participant has been disconnected past the grace period with nobody left (`reconnection/grace-period.md`)" — attributing the empty-room teardown to this feature. In practice that teardown (Postgres write, `race_finished` broadcast, actor shutdown, `Registry` removal) is `finish-race.md`'s "zero participants remaining active" finish condition, not something implemented here. This feature's only contribution is producing the `ParticipantLeft` event that can trigger it.
- Grace period length (30s suggested in §4.3) should be a named constant, not hardcoded inline — easy to tune later without hunting for magic numbers, but not worth making it configurable per-race until there's an actual reason to.
