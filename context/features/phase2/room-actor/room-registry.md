# Room Registry

## Overview

`room-actor/room-actor-core.md` defines what one room actor does; this spec covers how the process finds the *right* one for a given `race_id` — spawning it when a race starts, handing WebSocket connections to it, and shutting it down once the race is over or empties out. In Phase 2 this registry is purely in-process (a map behind a mutex); the Redis-backed cross-instance version (`room:<id> → instance:<id>`) is explicitly Phase 4 (context/project-overview.md §5) and out of scope here.

## Requirements

### Spawning

- A `RoomActor` is spawned the moment `POST /races/{id}/start` flips a race to `active` (Phase 1's existing endpoint, `internal/race`) — the registry needs a hook at that point, not a new endpoint of its own
- Spawning constructs the actor with the race's already-generated `prompt_text` and `distance_meters` (read once via the existing `RaceRepository.GetRace`/`GetRaceText`), then calls `go actor.Run()`
- A race that's still `pending` has no room actor yet — `GET /ws` (`websocket/ws-endpoint.md`) for a `pending` race must reject the connection rather than create one prematurely

### Lookup

- `Get(raceID string) (*RoomActor, bool)` — used by the WebSocket endpoint to find the actor to attach a new connection to
- Lookup must not race with spawn/remove — a `sync.RWMutex` guarding the underlying map is enough for Phase 2's single-instance scope; this is intentionally simpler than what Phase 4's Redis registry will need

### Teardown

- A room actor is removed from the registry when its race finishes (`race-completion/finish-race.md` triggers this) or when every participant has been disconnected past the grace period with nobody left (`reconnection/grace-period.md`) — either path calls the actor's `cancel()` and deregisters it
- Removal must not leak the goroutine: `cancel()` unblocks `Run()`'s `ctx.Done()` case, and `cleanup()` (mentioned but not detailed in `room-actor-core.md`) is where any per-connection resources still attached get closed

## Concurrency

- The registry itself must never become a second place where room state can be mutated — it only ever holds `*RoomActor` pointers and dispatches to them; it does not read or write `participants` directly (that would violate room-actor-core.md's single-writer principle)
- Tests here should include: concurrent `Get` calls during a spawn, and a `Remove` racing with an in-flight `Get` — exactly the kind of scenario `go test -race` exists to catch

## Data

```go
type Registry struct {
    mu    sync.RWMutex
    rooms map[string]*RoomActor
}

func NewRegistry() *Registry
func (reg *Registry) Spawn(ctx context.Context, raceID, promptText string, distanceMeters int) *RoomActor
func (reg *Registry) Get(raceID string) (*RoomActor, bool)
func (reg *Registry) Remove(raceID string)
```

## Notes

- One `Registry` instance lives for the process lifetime, constructed once in `internal/app.go` alongside the DB pool, and passed into whatever wires up `websocket/ws-endpoint.md` and the `race/start-race` handler's post-start hook — mirrors how `pool *pgxpool.Pool` is already threaded through `httpserver.RegisterRoutes`.
- Phase 4 replaces this with Redis-backed ownership (`SET room:<id> instance:<id> NX EX 60`) so multiple instances can agree on who owns a room — this spec's in-memory `Registry` is deliberately the simplest thing that works for one instance, not a placeholder abstraction pre-built for that future need (per this project's "don't design for hypothetical future requirements" convention).
