# Redis Room Registry (Ownership Only)

## Overview

`context/project-overview.md` §5: "You need a way to know which instance is
running which room — use Redis (`SET room:<id> instance:<id> NX EX 60`,
refreshed periodically) as a simple registry." This spec is exactly that,
and only that — it makes every instance able to answer "do I own this room,
and if not, who does?" correctly. It deliberately does **not** make
cross-instance traffic actually work yet (a client attached to a
non-owning instance still gets a 404/miss after this spec) — that's
`cross-instance-relay.md`, which depends on this one existing first.

No leader election is needed here, which keeps this spec smaller than it
might look: a room has exactly one instance that ever calls
`Registry.Spawn` for it — whichever instance happened to receive the
`POST /races` request that created it (`internal/race/handler.go:74`) —
so "ownership" is decided by construction, not contested. Redis's only job
is to make that fact durably visible to every *other* instance.

## Current state (confirmed by reading the code, not assumed)

- `internal/room/registry.go`'s `Registry` is an in-memory
  `map[string]*RoomActor` guarded by a `sync.RWMutex` — and must stay that
  way. The actual `RoomActor` goroutine can only ever run on one process;
  Redis records *where*, it doesn't (and can't) relocate the goroutine
  itself.
- `Registry`'s own doc comment already anticipates this spec: "a
  `sync.RWMutex` is enough for this single-instance phase; the Redis-backed
  cross-instance registry is Phase 4's concern, not this one's."
- Two call sites currently assume `Registry.Get` always finding a local
  actor if the race exists at all: `internal/ws/endpoint.go`'s
  `ServeHTTP` (line ~93) and `internal/race/handler.go`'s `Start` (line
  ~188). Both currently treat a `Get` miss as "race not found" — correct
  today, wrong the moment a second instance exists. This spec does not fix
  either call site (that's `cross-instance-relay.md`'s job); it only
  builds the Redis-side machinery those fixes will call into.

## Requirements

### Instance identity

- New `Config.InstanceID string` (`internal/config/config.go`), env
  `INSTANCE_ID`. If unset, generate one at startup — reuse
  `internal/race.GenerateRaceID`'s existing base58/`crypto/rand` approach
  (already proven, no new randomness scheme needed) rather than something
  hostname-based, since a local `go run` and a Kubernetes pod both need a
  value that's unique *per process*, and a Kubernetes pod's hostname is
  already unique on its own but a locally-run second instance's hostname
  is not.
- This becomes a genuinely useful value beyond this spec: it's also worth
  threading into the process-wide `slog.Logger`
  (`structured-logging.md`'s already-shipped pattern) as a static attribute
  so every log line from a given instance is identifiable in a
  multi-instance setup — call this out as a one-line addition in
  `internal/app.go`, not a reopening of that feature.

### Redis client

- New dependency: `github.com/redis/go-redis/v9`.
- New `Config.RedisURL string`, env `REDIS_URL`, default
  `redis://localhost:6379/0`.
- New `internal/redisclient` package (sibling to `internal/db`, same
  shape): `NewClient(ctx context.Context, url string) (*redis.Client,
  error)` — parses the URL, constructs the client, and `Ping`s it before
  returning, mirroring `internal/db.NewPool`'s exact error-wrapping
  convention (`fmt.Errorf("redisclient: ping: %w", err)`).

### The registry itself

- New package `internal/roomlocator` (not inside `internal/room` — keeps
  `internal/room` free of Redis imports, the same reasoning that already
  keeps it free of HTTP imports per `room-actor-core.md`'s Notes section).
- `Locator` struct wrapping `*redis.Client` and this instance's
  `InstanceID`.
- `Claim(ctx context.Context, raceID string) error` — `SET room:<raceID>
  instance:<instanceID> NX EX 60`. Called once, from `Registry.Spawn`,
  right after the actor is registered in the local map. If the key already
  exists (a `SETNX`-style false — vanishingly unlikely given ownership is
  decided by construction, not contested, but a real key-collision-with-a-
  stale-entry case is possible if two instances somehow raced to create
  the exact same `raceID` — cannot happen today since `raceID` is
  server-generated per `internal/race.GenerateRaceID`, not client-supplied)
  — log a warning and proceed anyway; this instance is still the one
  actually running the goroutine and the only real source of truth, a
  Redis key inconsistency here is a visibility problem, not a correctness
  one.
- `Refresh(ctx context.Context, raceID string) error` — re-issues the same
  `SET ... EX 60` (not a bare `EXPIRE`, so a Redis-side eviction between
  heartbeats can't leave the key present-but-valueless) on a heartbeat
  loop, started as a goroutine from `Registry.Spawn` alongside the existing
  `cleanupWhenDone` goroutine, ticking well inside the 60s TTL (e.g. every
  20s — 3 refreshes per TTL window is a reasonable safety margin against
  one missed tick under load) and stopped via the same `actor.Context()`
  the existing cleanup goroutine already watches — no new lifecycle
  primitive needed, reuse `RoomActor.Context()`.
- `Release(ctx context.Context, raceID string) error` — `DEL room:<raceID>`
  (guarded with the Lua `GETDEL`-if-owned-by-me pattern, or a simple
  compare-then-delete, so this instance never deletes a key some *other*
  instance's restart already re-claimed for the same `raceID` — extremely
  unlikely given `raceID` uniqueness, but cheap to guard against and worth
  doing correctly the first time rather than assuming it away). Called from
  `Registry.cleanupWhenDone`, right where it already removes the local map
  entry — without this, a room that finishes in 5 seconds would still show
  as "owned" in Redis for up to the remaining 55 seconds of its TTL, a real
  (if bounded) staleness window for whatever `cross-instance-relay.md`
  builds on top of this.
- `Owner(ctx context.Context, raceID string) (instanceID string, ok bool,
  err error)` — `GET room:<raceID>`, parses out the instance id. This is
  the method `cross-instance-relay.md` calls on a local `Registry.Get`
  miss to find out where to relay to; unused by anything in this spec's
  own scope, included here because it belongs with the rest of this
  read/write surface, not because this spec calls it itself.

### Wiring `Registry` to `Locator`

- `Registry` gains a small structural interface,
  `RoomLocator` (defined in `internal/room`, mirroring the existing
  `TickObserver` pattern exactly — `internal/roomlocator.Locator` satisfies
  it structurally, `internal/room` never imports `internal/roomlocator` or
  `redis` directly):

  ```go
  // internal/room/registry.go
  type RoomLocator interface {
      Claim(ctx context.Context, raceID string) error
      Refresh(ctx context.Context, raceID string) error
      Release(ctx context.Context, raceID string) error
  }
  ```

- `NewRegistry` grows one more parameter, `locator RoomLocator` — same
  "grows a constructor parameter, mechanical test-fixture churn across
  every package that constructs a `Registry`" cost `structured-logging.md`
  and `prometheus-metrics.md` already both paid for their own additions;
  same acceptable tradeoff here.
- **Local single-instance dev/tests need this to be a no-op.** Add a
  `NoopLocator` (`internal/room`, exported, three no-op methods returning
  `nil`) so every existing test's `newTestActor()`/`NewRegistry(...)` call
  site (there are many, per `context/current-feature.md`'s own history of
  how many features already touched this fixture) needs exactly one added
  argument, not a real Redis connection — confirm at `start` whether
  `NoopLocator` lives in `internal/room` itself (simplest, zero new
  package for a 3-method stub) or `internal/roomlocator` (keeps every
  Locator-shaped thing in one package) — leaning toward `internal/room`
  since that's where `RoomLocator` the interface is defined, and a
  no-op implementation of your own interface belongs next to it.

## Concurrency

- `Claim`/`Refresh`/`Release` each make one round-trip Redis call; none of
  them touch `Registry`'s own `sync.RWMutex`-guarded map, so nothing here
  changes `Registry`'s existing locking behavior — this spec only adds
  calls *around* the existing `Spawn`/`cleanupWhenDone` methods, not new
  contention on the map itself.
- The heartbeat goroutine started per room is a real, new per-room
  goroutine — worth confirming with `go test -race` (as always) and worth
  a quick sanity check that a room that lives for hours (not expected in
  this project, but not impossible) doesn't produce heartbeat-goroutine
  buildup: there's exactly one per live room, torn down via the same
  `actor.Context()` signal every other per-room goroutine in this codebase
  already uses, so this should fall out for free rather than needing new
  machinery.

## Data

```go
// internal/roomlocator/locator.go
type Locator struct { /* *redis.Client, instanceID string */ }
func NewLocator(client *redis.Client, instanceID string) *Locator
func (l *Locator) Claim(ctx context.Context, raceID string) error
func (l *Locator) Refresh(ctx context.Context, raceID string) error
func (l *Locator) Release(ctx context.Context, raceID string) error
func (l *Locator) Owner(ctx context.Context, raceID string) (instanceID string, ok bool, err error)

// internal/room/registry.go
type RoomLocator interface {
    Claim(ctx context.Context, raceID string) error
    Refresh(ctx context.Context, raceID string) error
    Release(ctx context.Context, raceID string) error
}
type NoopLocator struct{}
```

## Testing

- `internal/roomlocator` gets its own test file. Confirm at `start` whether
  a real local Redis (already available via a Redis service added to
  `docker-compose.yml` by this spec — see Notes) is required for these
  tests or whether `miniredis` (an in-memory fake, a new test-only
  dependency) is worth adding instead — this project's existing convention
  for Postgres is "no test files in `internal/postgres` at all, tested
  through the fake `RaceRepository` at the service layer instead"
  (`coding-standards.md`), so a real-Redis-required test suite here would
  be a new pattern; leaning toward `miniredis` to keep `go test ./...`
  runnable without any real infra, matching how every other package's
  tests already run.
- `internal/room`'s existing test suite: confirm `NoopLocator` slots into
  every existing `newTestActor()`-style fixture with no behavior change —
  this should be close to a pure mechanical update, not new test logic.

## Notes

- Redis added to `docker-compose.yml` (`redis:7-alpine`, no auth for local
  dev, matching Postgres's already-low local-dev security bar) — needed by
  this spec to have anything to `Claim`/`Refresh`/`Release` against at all,
  even before `multi-instance-dev-setup.md` (which is about running a
  *second backend instance*, not about Redis existing at all).
- This spec intentionally does not change `Registry.Get`'s behavior or
  either of the two call sites listed under "Current state" above — after
  this spec ships, a two-instance setup still behaves exactly as broken as
  it does today for cross-instance traffic. That's expected; it's the next
  spec's job, kept separate so this one stays reviewable on its own.
