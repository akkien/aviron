# Multi-Instance Local Dev Setup & Verification

## Overview

The real acceptance test for `redis-room-registry.md` +
`room-message-bus.md` + `room-service-adapter.md` + `ws-gateway.md`
together: N real `ws-gateway` processes in front of M real `cmd/server`
(`race-service`) processes, run locally as plain OS processes (no
Kubernetes — `context/features/phase5/` is deleted and deliberately not
recreated until this design is proven this way first, same sequencing
`phase-4-plan.md` and the original `phase-5-plan.md` both already
established). Genuinely different acceptance bar from the previous
(`race-router`) version, not just a rename — see "What's actually being
proven now, that wasn't before" below.

## Current state (confirmed by reading the code)

`load/multi-instance-check.sh`, `load/multi-instance-check.md`, and the
k6 scenarios it drives (`load/scenarios/multi-instance-check.js`,
`load/scenarios/multi-instance-reconnect-check.js`) all still exist and
are fully written — for the previous, now-removed `race-router` design.
They are the concrete starting point for this spec's own script, not a
green field: most of the process-management scaffolding (build real
binaries not `go run`, `exec` inside subshells so `$!` is the real killable
PID, `check_ports_free`, `wait_for_http` retry loops, the Postgres/Redis
readiness wait — now also waiting on NATS, per `room-message-bus.md`'s
transport switch) is topology-independent and ports over unchanged. What
needs real rework is everything that assumed *the client's own connection
already sits on the owning instance* — which was `race-router`'s whole
point, and is no longer true of any connection under this revision.

## What's actually being proven now, that wasn't before

`race-router.md`'s version only had to prove one thing: a client that
only ever talks to the router reaches the room's actual owner, whichever
instance that is. Correctness inside a room was then trivial — real
callers' WS connections *were* local to `RoomActor`, same process, same
memory. This revision removes that guarantee entirely: **every** WS
connection is local to whichever `ws-gateway` happened to accept it, with
zero relationship to which `race-service` instance owns the room. The
thing that actually needs proving now is that two participants in the
same race, connected through two *different* gateways, to a room owned by
either backend, still see fully consistent, correctly-ordered real-time
state — purely because the bus carried it correctly. That's a strictly
harder property than the one being tested before, and it's the one this
spec's script has to be rewritten to actually exercise, not just relabel.

## Requirements

### Topology

```text
server-A (:8080)   server-B (:8081)      -- race-service, unchanged
gateway-1 (:9090)  gateway-2 (:9091)     -- new: 2 ws-gateway instances,
                                             each RACE_SERVICE_INSTANCES=
                                             localhost:8080,localhost:8081
nats (:4222)                              -- new: message bus (room-message-bus.md),
                                             both server-A/B and both gateways
                                             point NATS_URL at it
```

Two gateways, not one — `race-router.md`'s original script only ever
needed a single router instance (it was a pure pass-through, so testing
two would have proven nothing extra). This revision specifically needs
**≥2 gateways** so the script can deliberately connect different
participants of the *same* race through *different* gateways — the exact
scenario described above. `nats` is new infrastructure this topology
didn't need before — `docker-compose.yml`'s `start_infra` step needs a
`nats` service (the plain `nats:latest` image is enough, no JetStream
configuration needed per `room-message-bus.md`'s "NATS Core, not
JetStream" decision) alongside its existing `postgres`/`redis`.

### Verification script, rewritten

Adapting `load/multi-instance-check.sh`'s existing structure:

1. `build_binaries`: `cmd/server` and `cmd/ws-gateway` (not
   `cmd/race-router`).
2. `start_backends`: start `server-A`/`server-B` exactly as today: then
   start **two** `ws-gateway` processes, both configured with
   `RACE_SERVICE_INSTANCES=localhost:8080,localhost:8081`, listening on
   distinct ports.
3. `register_and_login`/race creation: unchanged in shape, but issued
   directly against **one specific gateway** per user — deliberately
   picking *different* gateways for different participants of the same
   race, where the previous script always used a single shared `$ROUTER`
   base URL for everyone.
4. Ownership assertion (`owning_instance_letter` via `redis-cli GET
   room:$race_id`): unchanged, the registry itself didn't change.
5. **New assertion, not in the previous script**: confirm the bus
   actually carried traffic, not just that clients happened to see
   correct results (which could otherwise mask a bug where, say, the
   test race is small enough that reconnect/retry logic silently papers
   over a broken relay). Options to decide at `start`: structured-log
   grep for a `roomrelay: published`/`roomrelay: received`-shaped line on
   both the owning `race-service` and each participating gateway
   (cheapest, consistent with this project's existing log-based
   cross-checks), or a raw traffic tap via the `nats` CLI's `nats sub
   "room.>"` — a genuine, if modest, upgrade over the Redis-based
   version's `redis-cli PSUBSCRIBE room:*` equivalent, since NATS'
   hierarchical subjects mean this one wildcard subscription captures
   every race's `in`/`out` traffic in a single readable stream, started
   before the race and killed after, for manual inspection on first-run
   debugging.
6. Full lifecycle check (join, start, telemetry, finish): unchanged
   externally — `k6`'s `multi-instance-check.js` still just needs
   `BASE_URL` per participant, now pointed at *each participant's own*
   gateway instead of one shared router URL. `k6`'s own VU-isolation
   constraint (`k6-load-test.md`'s already-established reasoning) means
   this was already naturally structured as "each VU gets its own base
   URL," so this is a small parameterization change, not a rewrite of the
   scenario itself.
7. Kill-test: **the expected outcome changes, and this is the most
   important finding this spec's own first real run needs to record, not
   assume.** See "A real gap this revision's kill-test will likely
   surface" below before writing this step's assertions.

### A real gap this revision's kill-test will likely surface

Under `race-router`, killing the owning instance broke the *proxied raw
socket* immediately — an unambiguous, fast signal every affected client
felt right away, which is exactly what `race-router.md`'s own kill-test
verified ("a fresh reconnect attempt... must eventually fail cleanly —
not hang"). Under this revision, killing the owning `race-service`
instance does **not** touch any client's actual connection — those live
on `ws-gateway` processes, which have no direct relationship to any
specific `race-service` instance at all. The only observable effect is
that NATS subject `room.{race_id}.out` simply stops receiving publishes.
Nothing in
this phase's specs so far (`room-message-bus.md`, `ws-gateway.md`) gives
a `raceHub` any way to notice "the room's actual owner died" — it just
sits there, subscribed, silent, forever, unless a client eventually gives
up and disconnects on its own (a client-side timeout this project's
frontend may or may not currently have).

**This spec doesn't invent a fix for that gap up front** — consistent
with this project's own established convention (`k6-load-test.md`'s
"don't pre-write that spec, scope it to whatever a real run actually
shows," `k8s-race-service-deploy.md`'s equivalent note): run the kill
test, observe exactly what happens (silent hang is the predicted
outcome, not yet a confirmed one), and record the real result. If it is
a silent hang, that becomes a concrete, scoped follow-up for
`room-message-bus.md`/`ws-gateway.md` to solve — likely a `raceHub`-side
staleness timeout (no message received within N× the expected 250ms tick
interval → re-verify via `Owner()` whether the room still has a live
owner, and if not, synthesize a `room_closed`-equivalent local signal
rather than waiting forever) — but that's a design decision for whichever
spec ends up owning it once this run confirms the gap is real, not
something to guess at blind here.

## Verification

- Repeated full-lifecycle runs (matching the previous script's
  `REPEAT_RUNS` pattern) with participants deliberately split across both
  gateways, confirming both `server-A`/`server-B` get seen as an owner
  across enough runs, and — new — confirming both `gateway-1`/`gateway-2`
  successfully relay for races they didn't happen to receive the
  creating request on.
- The kill test, run to observe and document the real outcome per above,
  not to assert a predetermined pass/fail.
- `go test ./... -race` and this script's own clean exit are both part of
  this spec's done bar, same as the previous version.

## Notes

- `load/multi-instance-check.md`'s existing runbook prose needs the same
  topology update as the script itself — two gateways, the
  different-gateways-same-race scenario explained up front, not left
  implicit.
- This is the spec where `room-message-bus.md`'s own closing note
  ("don't treat either side's unit tests as sufficient proof the wire
  format actually round-trips") gets its real answer — treat a clean run
  here as the actual milestone that closes out this phase's horizontal-
  scaling work, not the individual specs' own unit tests.
