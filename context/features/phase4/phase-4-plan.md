# Phase 4 — Horizontal Scaling & Event Pipeline (WS Gateway revision)

## Overview

**This replaces the previous Phase 4 spec set in full** — every file
under `context/features/phase4/` was deleted and is being rewritten here,
not incrementally patched. The previous set built and shipped a working
design (`redis-room-registry.md` + `race-router.md`, both actually
implemented — `internal/roomlocator`, `internal/racerouter`,
`cmd/race-router` all exist in the codebase today); this revision
replaces `race-router`'s "route once, then local" approach with a genuine
WS Gateway + Message Bus split, per `docs/knowledge-summary.md`'s new
"Revision: adopting a WS Gateway tier" subsection (inside `##
Horizontally Scaling`) — read that section first, it's the authoritative
design reference every spec below points back to, including its diagram.

**Why the revision, stated plainly (not re-litigated here):** this
project's own architecture research (`docs/knowledge-summary.md`'s
"Why not every component fits this project" table) concluded a typing
race doesn't structurally *need* a separate connection-holding tier —
room count and connection count scale together, `RoomActor.Run()` is a
cheap 250ms `select` loop. That conclusion isn't wrong and isn't being
overturned. This revision is a deliberate choice to build the
higher-fidelity, harder pattern anyway, for what it teaches — per-message
relay across a bus, message ordering across process boundaries,
eviction-mirroring, a connection tier that's never the same process as
the simulation tier — because that's exactly what `project-overview.md`
§0/§1 and the JD this whole project targets are about. Treat this plan's
job as building that pattern correctly, not as re-arguing whether to
build it.

## A real reversal this plan makes to `project-overview.md`

`project-overview.md` §2/§8 say plainly: "this project has no separate
API Gateway service — race-service serves REST and WebSocket itself."
`race-router.md` (superseded) was careful to say it didn't reopen that
decision, because it never terminated the WebSocket. **This revision
does reopen it.** `ws-gateway` (below) terminates `GET /ws` itself —
decodes and encodes the protocol, holds the live connection — which is
exactly the thing §2/§8 said this project would never build. This is
flagged here explicitly rather than silently: `project-overview.md`
itself is out of this plan's scope to edit (it's the project's own
top-level source of truth, not a `context/features/` spec), but whoever
picks this plan up should update §2/§8 to describe the new architecture
once this phase ships, the same way `race-router.md` once updated §8's
"no separate API Gateway" framing for its own (smaller) change. Noted
here so it isn't lost.

## What's changing vs. what's staying

| Piece | Status | Notes |
| --- | --- | --- |
| `cmd/race-router`, `internal/racerouter` | **Removed as part of this phase** | Replaced by `cmd/ws-gateway`, `internal/wsgateway` — see `ws-gateway.md` |
| `internal/roomlocator` | **Unchanged, reused as-is** | See `redis-room-registry.md` — still the registry client, still `SET room:<id> instance:<id> NX EX 60` |
| `internal/room` (`RoomActor`) | **Modified** | Single-writer event-application logic (`applyEvent`) is untouched; only its `inbox`/`broadcast` I/O plumbing changes — see `room-service-adapter.md` |
| `internal/ws` | **Split** | Protocol types/decoding (`protocol.go`) move to wherever they're actually used now (see `room-service-adapter.md`'s "Current state" section for the concrete call); connection-handling code (`hub.go`, `endpoint.go`'s reader/writer loops) moves into `internal/wsgateway` — see `ws-gateway.md` |
| `internal/roomrelay` (new) | **New package** | The message bus itself — see `room-message-bus.md`. This is the package name `phase-5-plan.md`'s docs once referenced by mistake, before anything by that name existed; it's real now, and it's the generalized revival of `cross-instance-relay.md`'s original (superseded, never-built) per-message relay design |
| NATS (new infrastructure) | **New dependency** | `internal/roomrelay`'s transport — see `room-message-bus.md`'s "Correction (this pass)" note. **Not** Redis pub/sub, despite that being this spec's own original choice; the registry (`redis-room-registry.md`) stays on Redis, unchanged — this is a separate piece of infrastructure, not a reuse of the existing Redis instance |
| `internal/kafka`, `internal/consumer`, `cmd/consumer` | **Unaffected** | The event pipeline doesn't care which tier terminates the WebSocket — see `event-pipeline/`, recreated faithfully below since nothing about its design changes |

## Specs, in build order

1. `horizontal-scaling/redis-room-registry.md` — unchanged in design from
   the previous phase (the code already exists and is being kept), but
   recreated here since its own spec file was deleted, and its "who
   consults it, for what" section is updated for the new architecture
   (REST routing + ownership claims only — WebSocket traffic no longer
   needs it).
2. `horizontal-scaling/room-message-bus.md` — the new `internal/roomrelay`
   package, on NATS: subject naming, payload envelopes, subscribe/
   unsubscribe lifecycle on both sides, delivery-guarantee tradeoffs.
   Built before the two things that depend on it.
3. `horizontal-scaling/room-service-adapter.md` — the `race-service`-side
   change: `RoomActor`'s `inbox` fed by a bus subscriber instead of a
   local WS reader, its `broadcast` channel drained by a bus publisher
   instead of `internal/ws.hub`'s local fan-out.
4. `horizontal-scaling/ws-gateway.md` — the new `cmd/ws-gateway` binary:
   REST reverse-proxying (reused design from the removed `race-router`),
   WebSocket termination, local per-race fan-out (a gateway-side
   `internal/ws.hub`-shaped component fed by the bus), and
   eviction-mirroring.
5. `horizontal-scaling/multi-instance-dev-setup.md` — the real
   acceptance test for 1–4: N `ws-gateway` + M `race-service` processes,
   run locally, proving cross-process room consistency by hand before any
   Kubernetes work (once `context/features/phase5/` is recreated — see
   below) touches this.
6. `event-pipeline/kafka-producer.md`, `event-pipeline/
   kafka-consumer-postgres-sink.md` — recreated faithfully, unaffected by
   the WS Gateway pivot. Sequenced last because nothing above depends on
   them and nothing about them depends on 1–5 either; kept in this phase
   only because they're part of the same "Phase 4" scope
   `project-overview.md` §12 defines.

## Dependency order

```text
redis-room-registry (unchanged design, recreated spec)
        |
        v
  room-message-bus (new: internal/roomrelay)
        |
        +-------------------------+
        v                         v
room-service-adapter         ws-gateway
(race-service side)          (new binary)
        |                         |
        +-----------+-------------+
                     v
        multi-instance-dev-setup
        (real proof both sides agree on the wire format)

event-pipeline/ (kafka-producer, kafka-consumer-postgres-sink)
        — independent of the chain above, sequenced last only for scope
          reasons, not a real dependency
```

`room-service-adapter.md` and `ws-gateway.md` can be built in either
order or in parallel once `room-message-bus.md`'s envelope format is
fixed — they're two independent consumers of the same bus contract, the
same way a client and server can be built in parallel once an API
contract is agreed. `multi-instance-dev-setup.md` is the first point
either side's assumptions about the other get proven against reality
instead of against a fake in unit tests.

## Explicitly out of scope

- **`context/features/phase5/` (Kubernetes).** Also deleted, intentionally
  not recreated yet — per the user's own framing, it depends on this
  phase's design being settled first (it was already sequenced after
  Phase 4 for exactly this reason; recreating it before `ws-gateway`
  exists would mean re-deriving another set of docs on top of a moving
  target).
- **Regional/multi-DNS routing, Matchmaking, Presence, dedicated
  per-room Game Server processes.** Same verdicts as before — see
  `docs/knowledge-summary.md`'s "Why not every component fits this
  project" table, none of which this revision changes. This project is
  adopting the WS-Gateway-terminates-connections *shape*, not the full
  internet-scale architecture around it.
- **Kafka as the message bus.** Kafka stays scoped to `event-pipeline/`'s
  durable, batched, analytics-oriented traffic (`workout.sample`/
  `race.finished`) — a fundamentally different traffic shape from the
  real-time room bus (ephemeral, per-race, latency-sensitive, provisioned
  and torn down constantly), and Kafka's topic/partition model doesn't
  fit "cheap, dynamic, per-entity channel" the way NATS subjects or Redis
  channels do. **Correction (this pass): NATS *is* the message bus now**,
  not Redis pub/sub as this plan originally chose — see
  `room-message-bus.md`'s "Correction (this pass)" note for the reasoning
  and `docs/knowledge-summary.md`'s "## Game Message Bus" section for the
  full comparison this decision is based on.
- **A synchronous request/reply protocol over the bus.** NATS Core
  supports native request/reply as a pattern (unlike Redis pub/sub, which
  has none built in) — noted as a real capability this design still
  doesn't use: nothing in this phase's design needs a Gateway to block
  waiting for a specific Room Service reply. See `room-message-bus.md`'s
  "Evicted-reconnect checks bypass the bus entirely" section for the one
  place this could have been tempting, and why a small piece of shared
  Redis state is used instead, deliberately not NATS request/reply.
