# Log/Trace Correlation

## Overview

Every binary already logs structured JSON via `slog` (`structured-
logging.md`, tagged `race_id`/`user_id`/`request_id`), but nothing ties a
log line to the trace `tracing/instrumentation.md` produces for the same
request. This spec closes that gap — once both exist, Grafana can pivot
from a slow span straight to the log lines that explain it (`phase-6-
plan.md`'s Overview, the whole reason Grafana is the correlation layer).
Depends directly on `tracing/instrumentation.md` — there's no `trace_id`
to inject into a log line before a trace actually exists.

## Requirements

### The real design question: how `trace_id` actually reaches a log line

`slog`'s idiomatic ctx-aware enrichment pattern (a custom `slog.Handler`
whose `Handle(ctx, Record)` pulls attributes out of `ctx`) only fires for
call sites using `InfoContext`/`ErrorContext`/`WarnContext` — but this
codebase's existing convention is ctx-**less** `logger.Info(...)`/
`logger.Error(...)` everywhere, confirmed directly in `cmd/server/run.go`,
`cmd/ws-gateway/run.go`, `cmd/consumer/run.go`, and `middleware.
RequestLog` itself (`logger.Info("http_request", fields...)`, no `ctx`
argument). A Handler-based approach would need a broad refactor —
switching every relevant call site to the `*Context` variants — not a
localized change.

`middleware.RequestLog` already establishes this project's actual
pattern instead: manually build a `[]any` of `slog.Attr`s from context
values right before the one `logger.Info` call, the same way it already
pulls `request_id` via `RequestIDFromContext`. Extending that same
pattern to `trace_id`/`span_id` (via `trace.SpanContextFromContext(ctx)`,
`go.opentelemetry.io/otel/trace`) is the minimal-diff option, consistent
with how this codebase already solved the identical problem for
`request_id`:

```go
// internal/middleware/requestlog.go — shape of the addition
if sc := trace.SpanContextFromContext(r.Context()); sc.IsValid() {
    fields = append(fields,
        slog.String("trace_id", sc.TraceID().String()),
        slog.String("span_id", sc.SpanID().String()),
    )
}
```

**Open question for `start`**: this covers the one REST request-scoped
log line `RequestLog` emits per request, but not every `logger.Info`
call inside `internal/room`/`internal/race`/etc. that happens to run
during a traced operation (e.g. a room actor's own per-tick logging).
Whether those need the same manual enrichment, or whether the REST-
level line plus the trace itself is enough correlation in practice, is
worth confirming once `tracing/instrumentation.md` is live and there's a
real trace to look at — not decided here.

### `ws-gateway`/`consumer`

Same pattern, applied at whatever their own equivalent per-request/per-
message log line is: `ws-gateway`'s WebSocket frame handling (paired with
`tracing/instrumentation.md`'s per-telemetry-message span) and
`consumer`'s per-batch-flush log line (paired with the `kafka.consume`
span covering that message).

### What this spec explicitly doesn't do

- Doesn't change `logging/efk-deploy.md`'s ingestion path — Fluent Bit
  ships whatever JSON `slog` already emits; adding `trace_id`/`span_id`
  fields is invisible to Fluent Bit itself, no parser change needed
  there.
- Doesn't add log-based span events (`span.AddEvent(...)`) — logs and
  traces stay two separate systems correlated by a shared ID, not merged
  into one, matching `phase-6-plan.md`'s architecture (logs go through
  Fluent Bit, traces through the Collector, deliberately not the same
  pipeline).

## Concurrency

- `trace.SpanContextFromContext` is a pure, stateless read off the
  `context.Context` already threaded through every call — no new
  synchronization, same as `RequestIDFromContext`'s existing pattern.

## Data

```go
// internal/middleware/requestlog.go (extended, not new)
// adds trace_id/span_id slog.Attrs when r.Context() carries a valid
// trace.SpanContext
```

## Notes

- Depends on `tracing/instrumentation.md` landing first — a hard
  ordering requirement, not just a sequencing preference, per `phase-6-
  plan.md`'s "Dependency order".
- `logging/efk-deploy.md` has no hard dependency on this spec beyond
  wanting `trace_id` already flowing before the correlation view
  (Kibana log search jumping to a Tempo trace by `trace_id`) is worth
  demoing — the two can be built in either order.
- Grafana's own "correlate logs and traces" story
  (`dashboards/grafana-deploy.md`) is what actually surfaces this in the
  UI — this spec only makes the data exist, it doesn't build the pivot
  itself.
