# k6 Load Test

## Overview

`context/project-overview.md` §10: "Load testing with
[k6](https://k6.io/) (supports WebSocket) or a hand-written Go load-test
client that opens hundreds of simulated connections." §12's Phase 3 line
adds the actual point of doing this: "load testing, fixing
backpressure/goroutine leaks **uncovered by** load testing" — this spec is
explicitly a diagnostic tool, not a one-time checkbox. It only pays off
with `observability/prometheus-metrics.md` and `observability/pprof.md`
already in place to observe what happens under load, which is why it's
sequenced after both in `phase-3-plan.md`.

**This spec does not include fixing whatever it finds.** Backpressure or
goroutine-leak fixes are real follow-up work, but what needs fixing (if
anything) is unknown until this actually runs against a real instance —
writing that spec now would mean speccing a fix for a bug that might not
exist, or missing the shape of one that does. See `phase-3-plan.md`'s
"Explicitly out of scope" section.

## Requirements

### Tool choice: k6

- k6 over a hand-written Go client, per §10's own preference ordering
  (k6 listed first) and its native WebSocket support (`k6/ws` /
  `k6/experimental/websockets`) — a full typing-race simulation is
  exactly a REST-setup-then-WebSocket-session shape k6 is built for, and
  using it avoids writing and maintaining a second load-generation
  codebase alongside the actual backend.
- k6 itself is not currently installed/vendored anywhere in this repo
  (confirmed: no `k6` references anywhere in `backend/`/`docker-compose.yml`)
  — this spec includes adding a `load/` directory with `.js` scripts, run
  via the standalone `k6` binary (documented in a new `make loadtest`
  target), not a Go dependency.

### Scenario: end-to-end race lifecycle

1. **Setup (once, in k6's `setup()`)**: register N users via
   `POST /auth/register`, log each in via `POST /auth/login` to get a
   JWT — mirrors exactly what a real client does
   (`frontend/src/lib/auth.ts`'s flow), not a shortcut that skips real
   auth.
2. **Per virtual user (VU)**:
   - One VU creates a race (`POST /races`, auto-joined as creator per
     `CreateRace`'s existing behavior) and starts it once enough others
     have joined (`POST /races/{id}/start`).
   - Every other VU joins via `POST /races/{id}/join`, gets a
     `session_token`, then opens `GET /ws?race_id=...&session_token=...`
     — the real WS handshake, not a mocked connection.
   - Once connected, each VU sends `join_race`, then a stream of
     `telemetry` messages simulating typing (`distance_m`
     incrementing, `pace_watt` computed the same way
     `TypingBox.tsx` does — cumulative words/elapsed-minutes — spaced
     0.4-2s apart per `project-overview.md` §4.2's own stated bound on
     realistic typing cadence, not machine-gunned as fast as possible,
     since unrealistically fast telemetry doesn't represent real load
     and would trip up `LastSeq`-based ordering in ways a real client
     never would).
   - Race ends naturally once every VU's simulated `distance_m` reaches
     the race's `distance_meters` target.
3. **Scale knobs, tunable per run**: number of concurrent races, VUs per
   race (bounded by `race.MaxParticipants` = 10, same as any real race),
   total VUs — start small (e.g. 5 races × 8 VUs = 40 connections) and
   ramp up across repeated runs rather than picking one fixed number
   blind; "how many users can one instance handle" (a question already
   raised and left open in this project's own history) is the thing this
   scenario exists to actually start answering with real numbers instead
   of estimates.

### What to capture per run

- k6's own built-in HTTP/WS metrics (request duration, error rate,
  connection duration) — free, no extra work.
- Cross-referenced against `observability/prometheus-metrics.md`'s 5
  metrics scraped during the same run (tick latency, goroutine count,
  channel buffer usage climbing, connection count) — this is the actual
  point: k6 shows *symptoms* (rising latency, dropped connections),
  Prometheus shows *why* (which internal resource is saturating).
- A goroutine count that keeps climbing **after** VUs disconnect (not
  just during the run) is the concrete signature of a goroutine leak —
  worth calling out explicitly as the one signal to watch most closely,
  since it's exactly what §12's "fixing backpressure/goroutine leaks"
  line is checking for.

### Realistic failure injection (stretch, not required for a first pass)

- A subset of VUs abruptly closing their WebSocket mid-race (simulating
  the disconnect/reconnect scenarios `reconnection/grace-period.md`
  already has dedicated backend tests for) — this spec's first version
  should focus on steady-state load; deliberate disconnect chaos is a
  natural follow-up once the steady-state numbers are understood, not
  bundled into the same first run.

## Concurrency

- N/A from this codebase's perspective — k6 runs as an external process
  generating load against a real running instance; it doesn't touch this
  project's own goroutines/channels directly. The concurrency being
  *tested* is everything Phase 2 already built (single-writer room
  actors, per-connection buffered channels, the hub fan-out) — this spec
  is the first time that machinery gets exercised by genuinely concurrent
  load instead of `go test -race`'s handful of goroutines per test.

## Data

```text
load/
  scenarios/
    race-lifecycle.js   # the scenario described above
  lib/
    auth.js             # register/login helpers, shared across scenarios
    ws-client.js         # join_race/telemetry message helpers
```

```makefile
# backend/Makefile addition (sketch)
loadtest:
    k6 run ../load/scenarios/race-lifecycle.js
```

## Notes

- Depends on `observability/prometheus-metrics.md` and
  `observability/pprof.md` being in place first — running this without
  them still produces k6's own metrics, but loses the "why" half of the
  point.
- Does **not** depend on `observability/structured-logging.md` or
  `observability/opentelemetry-tracing.md` strictly, though structured
  logs make reading server-side output during/after a run meaningfully
  easier.
- The actual backend needs to be running against a real Postgres for
  this to mean anything — `docker-compose.yml` (now including `pgadmin`)
  already covers local Postgres; no new infra needed to run this against
  a single local instance. Testing actual horizontal scaling behavior
  under load is Phase 4 territory (Redis, ≥2 instances) — this spec is
  scoped to "how far does one instance go," matching where this project
  currently is.
- **Follow-up work this spec deliberately does not include**: whatever
  backpressure/goroutine-leak fixes an actual run surfaces. Write that as
  a new spec *after* running this and having a concrete finding to point
  at — not before.
