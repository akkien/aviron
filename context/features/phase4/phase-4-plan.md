# Phase 4 — Strong Plus (Horizontal Scale, Kafka)

## Overview

Per `context/project-overview.md` §12: "Run multiple instances + Redis
pub/sub for cross-instance sync. Add a Kafka → ClickHouse event pipeline for
the leaderboard... Move the entire stack onto local Kubernetes."
**ClickHouse is explicitly dropped from this project** — every place
§6/§12 names it as the pipeline's sink, this phase substitutes Postgres
instead (see `event-pipeline/kafka-consumer-postgres-sink.md`'s Overview
for the specific reasoning). **Kubernetes is broken out into its own
`phase5/`**, not part of this phase at all — it's deployment
orchestration, not application logic, and depends on this phase's
`horizontal-scaling/` work being done and proven first; see
`context/features/phase5/phase-5-plan.md`. **The internal gRPC service
(§8) is dropped entirely**, not deferred as optional — it had no product
need (the frontend already gets live rankings over WebSocket, and a
browser can't reach raw gRPC without infrastructure this project isn't
adding) and existed only to check a JD box; see "Explicitly out of scope"
below. Everything else in §5/§6 stays in scope here.

This is the largest application-logic phase in the project by a wide
margin — two genuinely different infrastructure concerns (Redis, Kafka),
several of which touch the same files. It is broken into 5 specs across 2
sub-areas specifically so no single spec tries to hold more than one
architectural decision at a time — a lesson from how large
`prometheus-metrics.md` (Phase 3) got trying to cover 5 metrics and 3 open
design questions in one file.

Every phase up to now assumed a single backend process: one in-memory
`room.Registry`, one in-process `hub` fan-out, one Postgres connection pool.
That assumption is load-bearing in more places than it looks — confirmed by
grep before writing any spec in this phase, not assumed from the doc
comments alone (`internal/room/registry.go`'s own comment already flags
this: "a `sync.RWMutex` is enough for this single-instance phase; the
Redis-backed cross-instance registry is Phase 4's concern, not this one's").
Phase 4 is what removes that assumption, one layer at a time.

## Specs, by sub-area

### `horizontal-scaling/` (§5) — do this first

**Design update, decided after this plan was first written (see
`docs/knowledge-summary.md`'s "Horizontally Scaling" section for the full
comparison and reasoning):** `cross-instance-relay.md` — originally the
second spec below, a per-message Redis pub/sub relay — is **superseded**
by `race-router.md`, a separate routing process that sends each connection
to the correct instance once, up front, instead of relaying every message
after the fact. It depends on the same `redis-room-registry.md`
foundation and slots into the same position in this list; nothing else
about the ordering below changed.

1. `redis-room-registry.md` — Redis becomes the source of truth for "which
   instance owns this room" (`SET room:<id> instance:<id> NX EX 60`,
   refreshed on a heartbeat), plus a `room:events` pub/sub channel
   (`created`/`removed`) added specifically for `race-router.md`'s cache.
   This alone does not make cross-instance traffic work; it only lets an
   instance (and now the router) answer "who owns this room?" correctly.
   Foundation for everything else in this sub-area.
2. `race-router.md` — the actual cross-instance behavior: a new
   `cmd/race-router` process routes every request/connection to the
   instance that owns its `race_id`, using a cache backed by #1. Depends
   on #1 — there's nothing to route by until an instance can be looked up.
   Replaces the originally-planned `cross-instance-relay.md` (kept on disk,
   marked superseded, for its historical design reasoning).
3. `multi-instance-dev-setup.md` — Redis added to `docker-compose.yml`, a
   documented way to run 2 local backend processes plus `race-router` in
   front of them, and a concrete verification plan (create through the
   router, connect through the router from either backend's own port,
   confirm the router always reaches the correct owning instance). Depends
   on #1 and #2 — this is where they get proven to actually work together,
   deliberately before `phase5/`'s Kubernetes work (`replicas: 2`) enters
   the picture at all.

### `event-pipeline/` (§6, ClickHouse dropped)

1. `kafka-producer.md` — the Race Service publishes to two topics,
   `workout.sample` (batched telemetry) and `race.finished` (final
   results), keyed for ordering per §6's own requirement. Independent of
   the Redis work technically, sequenced after it because horizontal
   scaling is the more load-bearing architectural change and this project's
   own convention (Phase 3's plan) is "foundation pieces first."
2. `kafka-consumer-postgres-sink.md` — a consumer group reads both topics.
   `workout_samples` (a table that has existed since the very first
   migration and has never once been written to — confirmed by grep, not
   assumed) becomes real. Depends on #1 (of this sub-area) existing to
   have anything to consume; otherwise independent of the Redis work.

## Dependency order

```text
redis-room-registry
       |
       v
race-router   (supersedes cross-instance-relay, kept on disk as historical record)
       |
       v
multi-instance-dev-setup
       |
       v
  (phase5/ — Kubernetes, once this chain is proven)

kafka-producer -> kafka-consumer-postgres-sink   (independent chain)
```

- `horizontal-scaling/` is the one dependency chain everything else needs
  proven before it matters (`phase5/k8s-race-service-deploy.md`'s
  `replicas: 2`) — build it first, in the 3-spec order above.
- `event-pipeline/` has no dependency on `horizontal-scaling/` and could
  technically be built in parallel with the Redis work — sequenced after
  it here only because this project's convention (every prior phase plan)
  is to finish one coherent sub-area before starting the next, not because
  of a real technical blocker.
- This entire phase is a prerequisite for `phase5/`, not the other way
  around — see that plan's own Overview for why Kubernetes is sequenced
  last across both phases combined.

## Explicitly out of scope

- **ClickHouse**, anywhere §6/§12 mention it — per explicit instruction.
  `kafka-consumer-postgres-sink.md` explains the substitution in full.
- **The internal gRPC service (§8, `GetLiveRankings`)** — dropped
  entirely, not built even in a minimal form. Reasoning worked through in
  conversation before this plan was finalized: the frontend already
  receives live rankings over its existing WebSocket connection
  (`race_state` broadcasts), so there was no consumer-side gap to fill;
  the one scenario that looked like it might justify gRPC — letting a
  non-participant "spectator" watch a race live without joining — turned
  out not to need gRPC either, since a browser can't call raw gRPC
  without a gRPC-Web proxy this project was never going to add, and the
  more direct fix for that scenario (if ever built) is relaxing the
  existing WebSocket's `session_token` requirement for a read-only
  attach, not standing up a second protocol. With no remaining use case,
  the service would have existed purely to check "gRPC is a plus" on the
  JD — not worth the new binary, protobuf toolchain, and dependency
  surface for that alone.
- **Kubernetes, entirely** — moved to `context/features/phase5/
  phase-5-plan.md`. Not a sub-area of this phase at all; see that plan for
  its own scope, including the `api-gateway`/CI-CD/Helm-chart decisions
  that used to live in this file before the split.
- **A real message-ordering stress test.** §6's ordering guarantee (same
  `race_id`/`user_id` key → same partition) is a Kafka property this phase
  configures correctly and can point at in code, not something that needs
  its own dedicated load-test spec the way Phase 3's `k6-load-test.md`
  covered WebSocket load — `load/`'s existing k6 scripts are not extended
  here.

## A note on scope discipline

This phase touches more files than any before it (excluding `phase5/`,
split out specifically to keep this file from also carrying deployment
concerns). Each spec should still be buildable and independently
verifiable (`go build`/`go test -race`/a concrete manual check) on its own
branch, per this project's normal `/feature` workflow
(`context/ai-interaction.md`) — resist the temptation to bundle two of
these specs into one feature the way some smaller phases bundled
same-package work, since the whole reason this plan has 5 specs instead of
3 is to keep each one small enough to actually reason about.
