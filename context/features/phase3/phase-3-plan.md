# Phase 3 — Production-Readiness

## Overview

Per `context/project-overview.md` §12: "Structured logging, Prometheus
metrics, pprof, load testing, fixing backpressure/goroutine leaks
uncovered by load testing." §9 spells out the observability requirements
in more detail; §10 covers testing strategy including load testing.

Every phase up to now (1 through 2.6, plus several standalone bug fixes)
built and hardened the actual product — auth, races, the room actor,
WebSocket protocol, reconnection, the pending lobby. None of it is
observable in production terms yet: no structured logs, no metrics, no
profiling, and — despite `context/current-feature.md`'s own history
recording an explicit "how many users can one instance handle?" question
being asked and left as "nobody knows yet, no load testing exists" — no
way to answer that question with real numbers. Phase 3 closes that gap,
directly matching the JD's "investigate and resolve production issues"
line this whole project exists to practice.

## Specs, in build order

1. `observability/structured-logging.md` — migrate the 7 existing
   `log.Printf` call sites to `slog`, add a `request_id` middleware
   (`internal/middleware`, matching `Auth`/`Cors`'s existing shape) and a
   per-request log line. Foundation for everything after it — load
   testing generates a lot of log output, worth having it structured
   before that starts.
2. `observability/prometheus-metrics.md` — `GET /metrics`, the 5 metrics
   §9 names by name: active room count, connection count, broadcast tick
   latency, goroutine count, channel buffer usage. New dependency
   (`prometheus/client_golang`). The largest spec in this phase — several
   open design questions flagged for `load`/`start` (how to read
   connection count and channel buffers without breaking
   `internal/room`/`internal/ws`'s existing "no HTTP imports" layering).
3. `observability/pprof.md` — `net/http/pprof` wired onto this project's
   real mux (not the stdlib default one, which this project doesn't use —
   a real gotcha flagged in the spec), gated behind a new
   `Config.PprofEnabled` bool. Smallest spec in this phase.
4. `load-testing/k6-load-test.md` — a k6 scenario simulating the full
   register → create/join race → WebSocket typing-race lifecycle, scaled
   across multiple concurrent races and participants. Sequenced after
   metrics + pprof deliberately: load testing without them still produces
   k6's own numbers, but loses the "why" half of the exercise.
5. `observability/opentelemetry-tracing.md` — **optional**, per §9/§1's
   own "optionally OpenTelemetry tracing" wording. Traces the join-race
   flow specifically (the deepest single-request call chain in this
   codebase: HTTP → service → repository → Postgres → in-memory actor).
   Sequenced last and explicitly skippable — nothing else in this phase
   depends on it.

## Dependency order

- `structured-logging.md` has no dependencies within this phase — it's
  the foundation, sequenced first for that reason, not because anything
  else strictly requires it to exist first.
- `prometheus-metrics.md` is independent of `structured-logging.md`
  technically (no shared code path that requires one before the other),
  but sequenced second since it's the other "day-one observability
  basics" piece.
- `pprof.md` is fully independent of both — could be built in any order,
  sequenced third mainly because it's the smallest and lowest-risk.
- `k6-load-test.md` depends on `prometheus-metrics.md` and `pprof.md`
  being in place first (see that spec's own Notes) — this is a real
  sequencing dependency, not just a suggested order.
- `opentelemetry-tracing.md` depends on nothing else in this phase and
  nothing else in this phase depends on it — it's the one spec here that
  can be dropped entirely without affecting any other spec's scope.

## Explicitly out of scope

- **Fixing whatever `k6-load-test.md` finds.** Backpressure or
  goroutine-leak fixes are real, likely follow-up work, but the shape of
  that work is unknown until a real run produces a real finding. Do not
  pre-write that spec — write it after running the load test, scoped to
  whatever it actually surfaces (or skip it entirely if nothing does).
- **Horizontal scaling / Redis / ≥2 instances** — still Phase 4
  (`project-overview.md` §5), unaffected by anything in this phase.
  `k6-load-test.md` is explicitly scoped to "how far does one instance
  go," not multi-instance behavior.
- **A real tracing backend deployment** (Jaeger/Tempo/etc.) — if
  `opentelemetry-tracing.md` is picked up at all, its own Notes section
  already flags that standing up somewhere to actually view traces is a
  bigger lift than the instrumentation code, and may mean stopping at a
  stdout exporter rather than a full backend.
- **Configurable log level filtering** (e.g. a `LOG_LEVEL` env var) —
  `structured-logging.md` defaults to `slog`'s standard `Info`-and-up
  behavior; adding a configurable minimum is a legitimate future
  nice-to-have, not required by §9's wording.

## Answering the open question

`context/current-feature.md`'s own history records this exact exchange:
asked how many users a single instance can handle and how to know when to
scale, the honest answer at the time was "nobody knows yet — no load
testing, no metrics." This phase is what turns that into a real, measured
answer: `prometheus-metrics.md` gives the signals (tick latency, channel
saturation, goroutine growth, connection count), `k6-load-test.md`
provides the load to observe them under. Worth revisiting that question
explicitly once this phase ships, with actual numbers instead of a
reasoned estimate.
