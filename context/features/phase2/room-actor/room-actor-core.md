# Room Actor Core

## Overview

The heart of Phase 2 (context/project-overview.md §4.1): each active race becomes one goroutine that owns all of that race's live state — participant positions, connection status, the tick loop. No other goroutine is ever allowed to mutate that state directly; every input, no matter which WebSocket connection it came from, is funneled through one channel. This is what makes the rest of Phase 2 (WebSocket, reconnection, race completion) safe to build without a web of mutexes.

This spec covers the actor itself as a pure state machine — applying events and producing snapshots. It does **not** cover wiring it to real WebSocket connections (`websocket/ws-endpoint.md`) or spawning/tracking instances per race (`room-actor/room-registry.md`).

## Requirements

### State

- `RoomActor` holds: `id` (race id), `participants map[string]*ParticipantState`, `promptText`/`distanceMeters` (copied in once at spawn time, from the already-`active` race row), `inbox chan RoomEvent`, `broadcast chan<- []byte`, and a `context.Context`/`context.CancelFunc` pair scoping the actor's lifetime
- `ParticipantState` holds: `UserID`, `DisplayName`, `WordsCorrect int` (mirrors `distance_m`), `LastSeq int` (last applied telemetry `seq`, for out-of-order/duplicate rejection per §4.2), `ConnectedAt`/`DisconnectedAt *time.Time` (the latter is `nil` while connected — `reconnection/grace-period.md` is what actually sets and reads it)

### Events

- `RoomEvent` is a small sum type (interface or tagged struct) with at least three variants: `ParticipantJoined{UserID, DisplayName}`, `TelemetryReceived{UserID, Seq, WordsCorrect}`, `ParticipantDisconnected{UserID}` — reconnection's `ParticipantReconnected` variant is added in `reconnection/grace-period.md`, not here
- `applyEvent(RoomEvent)` is a pure-ish method on `RoomActor`: given the current `participants` map and an event, produce the next map. Kept as a small, dependency-free function specifically so it's unit-testable without a running goroutine (mirrors how `internal/race/prompt.go`'s `generatePromptText` was kept pure and separate from the handler in Phase 1)
- A `TelemetryReceived` event is only applied if `Seq > participants[UserID].LastSeq` — anything else (retried/duplicate/out-of-order after a reconnect) is silently dropped, per §4.2's ordering rule

### Ticking / Broadcast

- `Run()` loops on a `select` between `<-r.inbox`, `<-ticker.C` (`time.NewTicker(250 * time.Millisecond)`, per §4.1's example), and `<-r.ctx.Done()`
- On each tick, `broadcastSnapshot()` builds one `race_state` message (§4.2) from the current `participants` map — `{tick, participants: [{user_id, distance_m, rank}, ...]}` — and sends it to `r.broadcast`. Rank is computed by sorting on `WordsCorrect` descending; ties keep insertion order (stable sort) rather than defining a tiebreaker nobody asked for
- On `ctx.Done()`, `Run()` calls `cleanup()` and returns — `cleanup()`'s actual responsibilities (closing per-connection channels, deregistering from the registry) belong to `room-actor/room-registry.md`, since this spec doesn't own connections or the registry

## Concurrency

- **Single-writer principle**: `participants` is never read or written by anything except the goroutine running `Run()`. Every other goroutine (WebSocket readers, the registry, HTTP handlers) only ever sends a `RoomEvent` on `r.inbox` — never reaches into `RoomActor` fields directly. `go vet`/code review should treat any exported mutable field on `RoomActor` as a smell.
- **Context for lifecycle**: `RoomActor.ctx` is a child of whatever created the registry entry (ultimately the server's root context), so a whole-process shutdown cancels every room actor without each one needing to be told individually.
- **Buffered `inbox`**: sized generously enough that a burst of telemetry from several participants typing at once doesn't block their WebSocket reader goroutines — an unbuffered or too-small channel here would turn "one slow room" into "one slow reader goroutine blocking everyone's typing."
- **`go test -race` is mandatory** for this package — this is the one piece of Phase 2 the JD weighs most heavily (context/project-overview.md §10), so tests here should include concurrent senders on `inbox` from multiple goroutines, not just single-threaded event application.

## Data

```go
type RoomActor struct {
    id           string
    participants map[string]*ParticipantState
    promptText   string
    distanceMeters int
    inbox        chan RoomEvent
    broadcast    chan<- []byte
    ctx          context.Context
    cancel       context.CancelFunc
}

type ParticipantState struct {
    UserID          string
    DisplayName     string
    WordsCorrect    int
    LastSeq         int
    ConnectedAt     time.Time
    DisconnectedAt  *time.Time
}

func NewRoomActor(id, promptText string, distanceMeters int, broadcast chan<- []byte) *RoomActor
func (r *RoomActor) Run()
func (r *RoomActor) applyEvent(ev RoomEvent)
func (r *RoomActor) broadcastSnapshot()
```

## Notes

- This package (`internal/room`, package name TBD at implementation time) has zero HTTP/WebSocket imports — it only knows about events in and snapshots out. That's deliberate: it keeps `go test -race` on this package fast and focused, and matches this project's existing layering convention (context/coding-standards.md) of keeping business logic free of transport concerns.
- `WordsCorrect`/`distance_m` naming follows the same "reuse the fitness-telemetry field names" convention established in context/project-overview.md §13 — nothing new to decide here, just carrying it forward into the room actor's internal state.
- Per context/project-overview.md §10, unit tests for `applyEvent` should be pure-function tests with no goroutines involved; the ticking/`Run()` loop gets its own concurrency-focused tests separately.
