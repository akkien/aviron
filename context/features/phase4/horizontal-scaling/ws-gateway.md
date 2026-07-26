# WS Gateway

## Overview

Replaces `race-router.md` (deleted along with the rest of the previous
Phase 4 set — see `phase-4-plan.md`). A new standalone process,
`cmd/ws-gateway` (`internal/wsgateway`), sits in front of the pool of
`race-service` instances, same position in the topology `race-router`
occupied. Two genuinely different jobs, not one:

1. **REST reverse-proxying** — unchanged in design from the removed
   `race-router`: room-scoped requests resolve via the registry's
   `Owner()` lookup and proxy to that instance; room-less requests
   round-robin. Reused wholesale, not redesigned.
2. **WebSocket termination** — the actual pivot this revision is about.
   `ws-gateway` does the `GET /ws` upgrade itself, decodes/encodes the
   protocol, and relays decoded messages over `room-message-bus.md`'s
   `internal/roomrelay` instead of proxying the raw connection through to
   whichever instance owns the room.

This is a materially different kind of component from `race-router`, and
from the "WS Gateway" tier `docs/knowledge-summary.md`'s large-scale
research describes — this one really is that tier now, deliberately,
unlike `race-router`'s explicit "not a gateway" framing. See
`phase-4-plan.md`'s "A real reversal this plan makes to
`project-overview.md`" section: this does reopen the "no separate API
Gateway" decision `project-overview.md` §2/§8 made, on purpose.

## Current state (confirmed by reading the code)

- `cmd/race-router` and `internal/racerouter` still exist and are fully
  functional, implementing the design this spec replaces — deleted as
  part of this spec landing, not left running alongside `ws-gateway`.
  `internal/racerouter`'s REST-proxying logic (`Director`, the routing
  cache, round-robin for room-less requests) is the direct starting point
  for this spec's REST half — read it before writing `internal/
  wsgateway`'s equivalent, most of it ports over unchanged.
- `internal/ws`'s connection-handling code (`WSHandler`, `hub`, `readLoop`
  /`writeLoop`, `session_token.go`) currently lives inside `race-service`
  and has no remaining caller there once `room-service-adapter.md` ships
  (`race-service` never accepts `GET /ws` again) — this spec is where
  that code actually goes, adapted for a bus-fed input/output.

## Requirements

### REST reverse-proxying (ported from `internal/racerouter`)

Unchanged behavior, reused implementation:

- `net/http/httputil.ReverseProxy` with a `Director` that resolves the
  target the same way: a request/path referencing a `race_id`
  (`/races/{id}/...`) consults a local routing cache (`race_id ->
  instance`, TTL-bounded, kept warm by `roomlocator.SubscribeRoomEvents`),
  falling back to a direct `Owner()` call on a cache miss; a request with
  no `race_id` (register, login, browse, leaderboard) round-robins across
  every configured backend.
- Genuine miss (registry doesn't know the room either) → `404`, not a
  forwarded request certain to fail.
- Static backend list (`RACE_SERVICE_INSTANCES`) for now, same as
  `race-router` had it — this project's existing scope stance against
  dynamic service discovery is unchanged by this revision; revisit only
  once `context/features/phase5/` (Kubernetes, not yet recreated) makes a
  static list genuinely unworkable, the same open question
  `race-router.md` originally flagged for that phase.

### WebSocket termination (new)

`GET /ws?race_id=...&session_token=...` is no longer proxied at all —
`ws-gateway` handles it directly, porting `internal/ws`'s connection-
handling logic with its `RoomActor`-facing calls replaced by
`internal/roomrelay` publish/subscribe calls:

1. **Verify the session token locally** — `verifySessionToken(token,
   jwtSecret)` (ported from `internal/ws/session_token.go`, unchanged
   logic) — meaning `ws-gateway`'s `Config` needs `JWTSecret`, something
   `race-router` never needed at all (it never inspected a token, see
   the "should race-router authenticate" reasoning this project already
   worked through — `ws-gateway` is different: it isn't a dumb
   byte-forwarding proxy anymore, it has to know *who* a connection
   belongs to in order to construct `InboundEnvelope`s, so verifying
   identity is now unavoidably this process's own job, not something it
   can defer to whichever backend receives forwarded bytes).
2. **Evicted-reconnect check** — `SISMEMBER race:{race_id}:evicted
   {user_id}` against Redis, synchronous, before ever upgrading the
   connection — see `room-message-bus.md`'s "Evicted-reconnect checks
   bypass the bus entirely" for why this is a direct Redis read, not a
   bus round trip. **Still Redis, unaffected by the message bus's NATS
   switch** — this check was never routed through `internal/roomrelay` in
   either version of the design.
3. **Upgrade the connection.** From here, `ws-gateway` owns the raw
   socket for the connection's entire lifetime — nothing about this step
   changes from what `internal/ws/endpoint.go` already does today, just
   relocated.
4. **Attach to this race's local fan-out** (see "Local per-race state"
   below) — register a per-connection outbound channel, same
   `connBufferSize`-bounded, drop-if-full design `internal/ws/hub.go`
   already uses, so one slow client's buffer filling up never stalls
   delivery to anyone else in the race.
5. **Reader goroutine**: decodes each inbound frame
   (`ws.DecodeClientMessage`, reused unchanged), publishes an
   `InboundEnvelope{Kind: message, ...}` onto NATS subject
   `room.{race_id}.in`. On read error/context-done, publishes
   `InboundEnvelope{Kind: disconnected, ...}` once before exiting — the
   bus-crossing equivalent of today's local
   `actor.Send(room.ParticipantDisconnected{...})`.
6. **Writer goroutine**: reads from this connection's registered outbound
   channel (fed by the local per-race fan-out, itself fed by the
   `room.{race_id}.out` subscription), writes each message verbatim to
   the socket — unchanged from `internal/ws/endpoint.go`'s existing
   `writeLoop`/`writeMsg`.

### Health check (`GET /healthz`) — closes a gap the removed `race-router` also had

`race-router` never exposed a health endpoint at all —
`multi-instance-check.sh`'s own workaround was treating "any HTTP
response, even 401, from `GET /races`" as proof of liveness, since
round-robin room-less requests were the only thing guaranteed to answer.
That's not good enough for `ws-gateway`: a real load balancer in front of
this pool (see the load-balancer design discussion this spec's Overview
assumes) needs an actual health signal, and `ws-gateway` depends on two
external systems failing either of which should take it out of rotation,
not just "the process is still running":

- `GET /healthz` — unauthenticated, no `Cors` wrapper, mirroring
  `cmd/server`'s existing `/healthz` shape (`internal/httpserver`'s
  `NewHealthzHandler`) so this project has one consistent health-check
  convention across every binary, not a bespoke one per process.
- Checks, synchronously, on every call: Redis reachable (a cheap `PING`
  — needed for `Owner()` REST routing and the evicted-reconnect check
  above) and NATS reachable (`nc.Status() == nats.CONNECTED`, or an
  equivalently cheap connection-state check — no request/reply round
  trip needed, `nats.go`'s client already tracks connection state
  locally without a network call). Both dependencies are load-bearing
  for this process's actual job (proxy REST correctly, relay WS
  messages correctly) — a `ws-gateway` that can reach neither is not
  meaningfully "up," even if its own process is technically still
  serving.
- No separate readiness/liveness split is proposed here (unlike
  `k8s-race-service-deploy.md`'s eventual treatment of `cmd/server`,
  once `context/features/phase5/` is recreated) — `ws-gateway` holds no
  long-running background state whose *liveness* could plausibly diverge
  from its *readiness* the way a room actor's tick loop could; one
  endpoint answering both questions is proportionate here. Revisit only
  if Kubernetes work resumes and finds a real reason to split them.

### Local per-race state: a gateway-side `hub`, fed by the bus

`internal/ws/hub.go`'s exact fan-out shape (register/unregister channels,
one goroutine as the sole mutator of the connection set, non-blocking
per-connection send that drops rather than blocks) is reused almost
verbatim — only its input source changes, from `RoomActor.Broadcast()`
directly to a `room.{race_id}.out` subscriber:

```go
// internal/wsgateway/racehub.go — one instance per race_id this gateway
// currently has >=1 local connection for
type raceHub struct {
    register    chan chan []byte
    unregister  chan chan []byte
    closed      chan struct{}
}

func newRaceHub(out <-chan roomrelay.OutboundEnvelope, done <-chan struct{}, onClose func()) *raceHub
```

- On `OutboundKindBroadcast`: fan `env.Payload` out to every registered
  connection, exact same non-blocking-send-or-drop logic `hub.run`
  already has.
- On `OutboundKindRoomClosed`: drain any already-buffered broadcasts
  (same ordering concern `room-service-adapter.md`'s "Ordering" section
  describes from the publishing side), then close `raceHub.closed` —
  every registered connection's writer goroutine observes this exactly
  like today's `writeLoop` observes `hub.closed`, drains its own
  connection channel, and returns, tearing the connection down.
- **Reference-counted subscription, new relative to `hub.go`**: a gateway
  process serves many races across many local clients, but should only
  hold a `room.{race_id}.out` subscription open for races it actually has
  a local connection for right now. `raceHub`s are created on this
  gateway's first local connection for a `race_id` and torn down (bus
  subscription cancelled) on its last local connection leaving — a
  `map[raceID]*raceHubEntry{hub, refCount}` guarded the same way this
  codebase already guards comparable state (`sync.RWMutex`, matching
  `race-router`'s own routing-cache shape and `room.Registry`'s).

### Config

```go
// internal/wsgateway/config.go
type Config struct {
    ListenAddr string
    RedisURL   string        // registry (Owner() lookups) + evicted-reconnect check
    NATSURL    string        // new — internal/roomrelay's transport (room-message-bus.md)
    JWTSecret  []byte        // new relative to internal/racerouter.Config — see "WebSocket termination" step 1
    Backends   []string
    CacheTTL   time.Duration
}
```

Same shape as the removed `internal/racerouter.Config` plus `JWTSecret`
and `NATSURL` — everything else (`RACE_SERVICE_INSTANCES`,
`RACE_ROUTER_LISTEN_ADDR`-equivalent, `REDIS_URL`, `ROUTING_CACHE_TTL`)
ports directly, confirm at `start` whether the env var names themselves
get renamed (`RACE_ROUTER_LISTEN_ADDR` → `WS_GATEWAY_LISTEN_ADDR`,
matching the binary's new name) or kept as-is to minimize
`docker-compose.yml` churn — either is defensible, lean toward renaming
since `docker-compose.yml` needs updating anyway once `cmd/race-router`
no longer exists to build (and gains a `nats` service either way).
`NATSURL` has no established env var name to preserve continuity with,
so `NATS_URL` (matching this project's existing `REDIS_URL`/
`DATABASE_URL`/`KAFKA_BROKERS` naming convention) is the natural choice,
not something to bikeshed at `start`.

## Data

```go
// internal/wsgateway/gateway.go
type Gateway struct {
    proxy    *httputil.ReverseProxy // REST — ported from internal/racerouter.Router
    routingCache // ported unchanged
    locator  RoomLocator            // internal/roomlocator (Redis) — Owner() lookups + evicted-reconnect check
    relay    *roomrelay.Relay       // new — internal/roomrelay (NATS) — see room-message-bus.md
    hubs     map[string]*raceHubEntry // new — this gateway's local per-race fan-out state
    hubsMu   sync.RWMutex
    jwtSecret []byte
    logger   *slog.Logger
}
```

## Concurrency

- REST-proxying concurrency properties are unchanged from `race-router`
  — `ReverseProxy` is already safe for concurrent requests, the routing
  cache's `go test -race` coverage ports over directly.
- Each WS connection still gets exactly 2 goroutines (reader, writer),
  same as today's `internal/ws/endpoint.go` — this property doesn't
  change just because they're now bus-connected instead of
  `RoomActor`-connected; both goroutines still exit cleanly on
  disconnect/context-cancellation, still coordinated via a `sync.
  WaitGroup` + shared `context.Context`, unchanged from `serveConn`'s
  existing pattern.
- `raceHub`'s subscribe-on-first-connection/unsubscribe-on-last-
  connection lifecycle needs its own `go test -race` coverage: concurrent
  connect/disconnect racing the ref-count transition to/from zero,
  ensuring a subscription is never torn down while a connection is still
  registered, and never left dangling after the last one leaves.

## Testing

- Unit: REST `Director` tests port from `internal/racerouter`'s existing
  suite unchanged.
- Unit: WS connection-establishment sequence (session token verify →
  evicted check → upgrade → attach to hub) against fakes for
  `roomlocator`/`roomrelay`/Redis/NATS — no real Redis, NATS, or
  `race-service` needed, mirrors this project's existing fake-repository
  convention.
- Unit: `raceHub`'s fan-out/drain/close sequence, direct port of
  `internal/ws/hub_test.go`'s existing test cases against the new
  bus-fed input.
- Unit: `GET /healthz`'s two-dependency check, against fakes reporting
  each of Redis/NATS up/down independently — confirm either one being
  down alone is enough to fail the check, not just both at once.
- Integration: a real `ws-gateway` in front of a real (or `miniredis`
  -backed) Redis, a real (or embedded, per `room-message-bus.md`'s Notes)
  NATS instance, and a fake `race-service` that just echoes bus messages
  back, confirming a client's `telemetry` frame round-trips to a
  `room.{race_id}.in` publish and a published `room.{race_id}.out`
  message round-trips to the client's socket.
- `multi-instance-dev-setup.md`: the real end-to-end proof, N gateways +
  M race-service instances.

## Notes

- **`ws-gateway` doesn't need `race-router`'s eventual "headless-Service-
  DNS backend discovery" open question resolved any differently** — that
  was flagged in the removed `race-router.md` for Kubernetes specifically
  (`context/features/phase5/`, also deleted, not yet recreated) and
  applies identically here: a static `RACE_SERVICE_INSTANCES` list is
  fine for local `go run`/`docker-compose`, and the same headless-Service
  question resurfaces once Kubernetes work resumes.
- Sits behind whatever terminates TLS in a real deployment, same as
  `race-router` did — this spec is the routing + WS-termination decision
  only, not a TLS/DDoS-protection/rate-limiting layer.
- **Two single-instance dependencies for now, both disclosed
  simplifications, not a new risk introduced by this spec**: a single
  Redis instance (`redis-room-registry.md`'s existing disclosure — this
  spec's `Owner()` lookups and evicted-check reads inherit that risk) and
  a single NATS instance (`room-message-bus.md`'s — this spec's
  `raceHub` subscriptions inherit that one). A real deployment of either
  piece would run its dependency clustered; this project runs one of
  each, deliberately, for local-dev simplicity, the same category of
  accepted risk already carried for Postgres.
