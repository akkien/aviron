# WebSocket Endpoint

## Overview

`GET /ws?race_id=...&session_token=...` (context/project-overview.md §8) — the actual connection clients open to join a live race. This spec covers the handshake, session-token verification, and the per-connection reader/writer goroutines that bridge a WebSocket connection to a room actor's `inbox`/broadcast channels (`room-actor/room-actor-core.md`). Message contents are `websocket/protocol.md`'s concern, not this one's.

## Requirements

### Endpoint

- `GET /ws?race_id=...&session_token=...` — upgrades the HTTP connection using `gorilla/websocket` or `nhooyr.io/websocket` (context/project-overview.md §11, either is acceptable)
- `session_token` is the per-race JWT `JoinRace` already issues in Phase 1 (`internal/race`, `race_id`/`user_id` claims, 6h TTL) — verified the same way `middleware.Auth` verifies the main JWT, just against this token's own claims instead of `sub`/`email`
- Reject the upgrade (before it happens, with a plain HTTP error) if: the token is invalid/expired, `race_id` in the token doesn't match the query param, or `room-actor/room-registry.md`'s `Get(raceID)` finds no running actor (race is `pending` or already `finished`)
- On successful upgrade, the connection is handed to the room actor found via the registry: an event is placed on its `inbox` marking this participant attached (or reattached, if `reconnection/grace-period.md` applies), and the goroutines below are started

### Connection Goroutines

- **Reader goroutine**: reads inbound frames, decodes via `websocket/protocol.md`, and forwards the resulting event onto the room actor's `inbox` — never touches room state directly, per `room-actor-core.md`'s single-writer principle
- **Writer goroutine**: owns a per-connection buffered channel that the room actor's broadcast fans out to; reads from it and writes frames to the actual WebSocket connection
- Both goroutines share one `context.Context` scoped to the connection (a child of the room actor's context) — when either the client disconnects, the room actor closes this room, or the process shuts down, cancelling that context is what stops both goroutines. Use `errgroup` or `sync.WaitGroup` + cancellation so one goroutine failing (e.g., a write error because the client vanished) doesn't leave the other blocked forever on a channel read

## Concurrency

- **Backpressure**: the writer's per-connection channel is buffered but bounded — if a slow client's buffer fills up (its network can't keep up with 250ms ticks), new broadcasts for that connection are dropped (and logged), not blocked on. One slow client's writer goroutine must never stall the room actor's `broadcastSnapshot()`, which is shared across every participant in the room.
- **No goroutine leaks**: every reader/writer pair started by this endpoint must have a corresponding exit path. Tests here should specifically open a connection, close it abruptly, and assert (e.g., via a goroutine-count check or explicit signal) that both goroutines actually exited — this is precisely the "goroutine leaks" pitfall context/project-overview.md §4.1 and the JD call out by name.
- **`go test -race` mandatory**, same as `room-actor-core.md` — this endpoint is where concurrent access patterns (many connections, one room actor) actually get exercised end to end.

## Data

- No new Postgres access here — this endpoint only talks to `room-actor/room-registry.md` (in-memory) and verifies the session token (no DB round-trip needed, it's a signed JWT). Depends on Phase 1's `internal/race` for the session-token-signing convention, but doesn't share code with it beyond that.

## Notes

- `websocket/protocol.md`'s `join_race` message is what actually triggers the "attach to room state, send one immediate snapshot" behavior described there — the query-string handshake above is just how the connection gets authenticated and routed to the right room actor in the first place.
- This spec assumes single-instance Phase 2 scope: the registry lookup always finds the room actor locally, because there's only one process. Phase 4's Redis-backed cross-instance routing (context/project-overview.md §5) is what makes "connect to any instance, get routed to the right one" work — not needed yet.
