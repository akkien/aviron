# Race Router

## Overview

Replaces `cross-instance-relay.md` (superseded — see that file's own
"Superseded" section for the full reasoning). Per `docs/knowledge-
summary.md`'s "Horizontally Scaling" section: a new standalone process,
`cmd/race-router`, sits in front of the pool of `race-service` instances
and routes every request/connection to the instance that actually owns
its `race_id` — **once, up front**, using `redis-room-registry.md`'s
registry as the source of truth. Depends on `redis-room-registry.md`
(needs `Owner` and the `room:events` pub/sub channel it publishes)
existing first.

Not the same kind of component as the "WS Gateway" tier in the large-scale
architecture `docs/knowledge-summary.md` researched, and deliberately not
built that way: `race-router` never terminates the application-level
WebSocket connection or runs any room logic. It's a routing/reverse-proxy
layer — the smart-load-balancer role — sitting in front of instances that
each still do REST + WS termination + room simulation together, unchanged.
This doesn't reopen the "no separate API Gateway" decision
(`project-overview.md` §8) — that decision was specifically about not
splitting WS termination from room simulation; a routing layer in front of
the whole pool is infrastructure every deployment needs *something* for,
whether that's this, nginx, or a cloud load balancer.

## Current state (confirmed by reading the code)

- No `cmd/race-router` exists yet. `internal/race/handler.go` and
  `internal/ws/endpoint.go` still assume whatever instance receives a
  request already owns the room in question — true today (single
  instance), and **stays true after this spec ships too**, for a reason
  worth stating plainly: because `race-router` proxies the connection
  itself to the correct instance before that instance ever sees the
  request, neither of those two files needs to change *at all* for the
  "wrong instance" case. This is the biggest simplification over
  `cross-instance-relay.md`'s superseded design, which needed changes to
  three call sites plus a new relay package inside `race-service` itself.

## Requirements

### The router process (`cmd/race-router`)

- New binary, own `main.go` (e.g. `cmd/race-router/main.go`), its own
  small `Config` (listen address, Redis URL, backend instance list, cache
  entry TTL).
- Backend discovery: a static, config-provided list of backend addresses
  (e.g. `RACE_SERVICE_INSTANCES=host1:8080,host2:8080`) is enough for this
  project's scale — no dynamic service discovery needed, consistent with
  this project's existing scope stance against adding a service mesh.
  Confirm at `start` whether Phase 5's Kubernetes work should instead feed
  this from a headless Service's DNS once that phase begins — not needed
  before then.

### Reverse proxy

- `net/http/httputil.ReverseProxy` with a custom `Director` that rewrites
  `req.URL.Host`/`Scheme` based on the routing decision below — chosen
  over hand-rolled proxying because `ReverseProxy` already correctly
  forwards a hijacked/upgraded connection once the backend responds `101
  Switching Protocols`; no bespoke socket-splicing code is needed for the
  WebSocket case.
- A request/connection with no room-scoping parameter (register, login,
  `GET /races` browse, `GET /leaderboard/me`, ...) is round-robin'd across
  every healthy backend — no registry lookup at all, since there's no
  `race_id` to route by.
- A request/connection that references a `race_id` (a path param on the
  REST routes, or the `race_id` query param on `GET /ws`) consults the
  routing cache below instead.

### Routing cache

- An in-memory map (`race_id → instance address`), guarded the same way
  this codebase already guards similar state (`sync.RWMutex`, matching
  `room.Registry`'s own shape) — confirm at `start` whether `sync.Map`
  reads more clearly here instead; default to `RWMutex` for consistency
  with the rest of the codebase unless profiling says otherwise.
- Populated two ways:
  1. **Lazily, on a cache miss** — a direct `Owner(ctx, raceID)` call
     against the registry (`redis-room-registry.md`), with the result
     cached under a short TTL (e.g. 30s: long enough to avoid hammering
     Redis repeatedly within one race's short lifetime, short enough to
     self-correct a stale entry even if a `room:events` message below was
     missed).
  2. **Proactively, via the `room:events` subscription** — one long-lived
     Redis pub/sub subscriber goroutine, running for the router process's
     entire lifetime, updating or evicting cache entries as
     `created`/`removed` events arrive (`redis-room-registry.md`'s
     `Claim`/`Release` publish these). This is the fast path; the TTL
     above is only the safety net for whatever this subscription misses
     (a dropped connection, a router restart).
- On a genuine miss — the registry also doesn't know the room (cancelled,
  finished and expired, or never existed) — the router returns `404`
  itself rather than forwarding a request that's certain to fail anyway.

### The WebSocket case specifically

`GET /ws?race_id=...&session_token=...` is proxied exactly like any other
room-scoped request — the router's job ends at picking the right backend
and letting `ReverseProxy` forward the upgrade unchanged. **No
`IsEvicted`/eviction-mirroring machinery is needed at all**, unlike
`cross-instance-relay.md`'s superseded design — because the WS handshake
now happens directly against the instance that actually owns the room,
`internal/ws/endpoint.go`'s existing local `actor.IsEvicted(userID)` check
already runs correctly, against the right in-memory state, with zero
changes required. This is the single biggest simplification this design
gets over the relay it replaces.

## Data

```go
// cmd/race-router/router.go
type Router struct {
    cache    cacheMap // race_id -> {instance string, expiresAt time.Time}
    locator  *roomlocator.Locator // Owner(); also subscribes to room:events
    backends []string
    next     atomic.Uint64 // round-robin counter for room-less requests
}

func NewRouter(locator *roomlocator.Locator, backends []string) *Router
func (rt *Router) Director(req *http.Request)
func (rt *Router) watchRoomEvents(ctx context.Context) // one subscriber goroutine, started at startup
```

`roomlocator.Locator` is reused as-is from `redis-room-registry.md` —
`race-router` imports the same `internal/roomlocator` package that spec
introduces; no second Redis-client package is needed.

## Concurrency

- `watchRoomEvents` is exactly one goroutine for the router's entire
  process lifetime — not per-room, not per-connection — subscribing once
  to the single shared `room:events` channel. Torn down on process
  shutdown (`SIGTERM` → root `context.Context` cancellation), the same
  pattern this codebase already uses everywhere else for a process-
  lifetime goroutine.
- The cache needs `go test -race` coverage: concurrent `Director` calls
  (reads) racing the subscriber goroutine's writes, and racing a
  cache-miss goroutine's own write-after-lookup — the same shape of test
  `room.Registry` already has for its own map.
- `ReverseProxy` itself is stdlib and already safe for concurrent use
  across requests; nothing new to prove there beyond the `Director`
  closure's own state access.

## Testing

- Unit tests for `Director`'s routing decision against a fake
  `RoomLocator` (mirrors this project's existing fake-repository testing
  convention — no real Redis needed): room-less request → round robin;
  cache hit → correct host, no `Owner` call; cache miss → `Owner` called
  once, cache populated, correct host; genuine miss → `404`.
- One integration-style test proxying a real `Router` in front of an
  `httptest.Server` pair (two fake backends), confirming both the
  round-robin and room-scoped paths land on the expected backend.
- `multi-instance-dev-setup.md` is this spec's real, higher-value
  acceptance test — running `race-router` in front of two real
  `cmd/server` processes and confirming a client connecting through the
  router lands on the correct instance regardless of which one currently
  owns a given room.

## Notes

- Sits behind whatever terminates TLS in a real deployment (nginx-ingress
  in `context/features/phase5/`, or nothing extra locally) — this spec is
  the room-aware routing decision only, not a TLS/DDoS-protection/
  rate-limiting layer; those stay the outer layer's job.
- **Single Redis instance for now, same disclosed simplification as
  `redis-room-registry.md`.** A real deployment of this design should run
  Redis as a Cluster with per-shard replication — this router's cache
  depends on the same registry being reachable that the owning instances
  do, so it inherits that spec's single-point-of-failure risk exactly, not
  a new one. See `docs/knowledge-summary.md`'s "Horizontally Scaling"
  section for the full reasoning.
- Graceful draining on scale-down (marking an outgoing instance
  unavailable for *new* room placement while it finishes rooms it already
  owns) means `race-router`'s `backends` list becoming dynamic instead of
  static — explicitly out of scope for this first version, worth its own
  follow-up spec once Phase 5's Kubernetes work makes instance count
  actually change at runtime.
