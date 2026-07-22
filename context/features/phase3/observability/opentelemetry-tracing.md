# OpenTelemetry Tracing (Optional)

## Overview

`context/project-overview.md` §9 lists this with an explicit qualifier —
"structured logs, Prometheus metrics, **optionally** OpenTelemetry
tracing" — and §1's in-scope list repeats the same word: "Observability:
structured logs, Prometheus metrics, **optionally** OpenTelemetry
tracing." Unlike the other 3 observability specs in this phase, this one
is explicitly a stretch goal, not a requirement. It's written up here so
the shape is decided *if* it gets built, not to imply it's expected before
`load-testing/k6-load-test.md`.

Scope, per §9: "tracing across REST + the join-race flow — useful for
'debugging production services with logs and metrics.'"

## Requirements

### Scope: the join-race flow specifically

- A trace spanning `POST /races` (or `POST /races/{id}/join`) →
  `RaceService.CreateRace`/`JoinRace` → the Postgres
  insert → `Registry.Spawn`/`RoomActor` accepting the new participant —
  the one flow in this codebase that crosses the most layers in a single
  user action (HTTP handler → service → repository → Postgres → in-memory
  actor), making it the most representative "does a request-scoped trace
  actually help debug this system" test case.
- Explicitly **not** attempting to trace the WebSocket connection's
  entire lifetime or every `telemetry` message — a trace models a
  request/response, and a long-lived WS connection with hundreds of
  messages doesn't fit that shape without inventing something more
  elaborate (e.g. a span per message) that §9's wording doesn't ask for.

### Propagation

- `request_id` from `structured-logging.md` and OTel's own trace/span IDs
  are two different identifiers serving two different tools — **open
  question for `load`/`start` if this spec is picked up**: attach
  `request_id` as a span attribute so a log line and a trace can be
  cross-referenced by a human, or treat them as fully separate and accept
  that correlation isn't built. Given this spec's own optional status,
  leaning toward "skip the cross-referencing wiring too" unless it turns
  out to be nearly free once both pieces exist.

### Exporter

- No tracing backend exists anywhere in this project's infrastructure
  (`docker-compose.yml` today: `postgres` + `pgadmin` only). Standing one
  up (Jaeger, Tempo, or similar) is arguably a bigger lift than the
  instrumentation code itself — **this spec should not be started
  without first deciding where traces actually go**, since
  `otel.SetTracerProvider` with no real exporter behind it produces
  spans nobody can ever look at. A `stdouttrace` exporter (prints spans
  to stdout as JSON) is the minimum viable version — genuinely useful for
  confirming instrumentation works, not for real debugging — and might
  be as far as this spec goes if a full tracing backend is judged not
  worth standing up for a side project.

## Concurrency

- OpenTelemetry's Go SDK is designed for concurrent use (context-
  propagated spans, safe span creation from multiple goroutines) — no
  new concurrency design needed beyond correctly threading `context.Context`
  through the join-race call chain, which this codebase already does
  end-to-end (every method in `internal/race`/`internal/room` already
  takes a `ctx` first param).

## Data

```go
// sketch only — exact API surface depends on go.opentelemetry.io/otel's
// version at whatever point this spec is actually started
tracer := otel.Tracer("aviron/race")
ctx, span := tracer.Start(ctx, "JoinRace")
defer span.End()
```

## Notes

- Depends on nothing else in this phase strictly, but makes the most
  sense built last — it's the most speculative of the 4 observability
  specs, and the other 3 (logging, metrics, pprof) are unambiguously
  required by §9's wording while this one isn't.
- **If time-constrained, skip this spec entirely** — re-read §9/§1's own
  "optionally" before starting it; nothing else in Phase 3 or
  `load-testing/k6-load-test.md` depends on it existing.
- New dependencies if built: `go.opentelemetry.io/otel`,
  `go.opentelemetry.io/otel/sdk`, plus whichever exporter package is
  chosen — none currently in `go.mod`.
