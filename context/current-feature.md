# Current Feature: Log/Trace Correlation

## Status

In Progress

## Goals

- Every REST request's `middleware.RequestLog` summary line
  (`http_request`) carries `trace_id`/`span_id` when the request's
  context holds a valid OpenTelemetry span — the same request already
  gets one from `otelhttp` (Distributed Tracing — Instrumentation,
  already shipped).
- `ws-gateway`'s per-frame `"wsgateway: published"` log line carries
  `trace_id`/`span_id` from that frame's own `ws.frame` span.
- `consumer`'s per-message `race.finished` log lines (`processRaceFinishedMessage`'s
  Warn/Error calls) carry `trace_id`/`span_id` from that message's own
  `kafka.consume` span — `workout.sample` is batched and explicitly
  excluded (see Plan).
- No change to `logging/efk-deploy.md`'s ingestion path and no
  `span.AddEvent(...)` log-into-trace merging — logs and traces stay
  two separate systems correlated only by a shared id string, per the
  spec's own explicit non-goals.
- Resolve the spec's one open question (whether `internal/room`'s own
  per-tick/per-event logging needs the same enrichment) — see Plan;
  answered by what Distributed Tracing — Instrumentation already built,
  not left open again here.

## Explain

- Every binary already emits structured JSON via `slog`, but nothing
  ties a log line to the trace `tracing/instrumentation.md` (already
  shipped) produces for the same request — this spec closes that gap
  so Grafana can eventually pivot from a slow span straight to the log
  lines that explain it.
- **The real design question, per the spec**: `slog`'s idiomatic
  ctx-aware enrichment (a custom `Handler` whose `Handle(ctx, Record)`
  pulls attributes off `ctx`) only fires for `InfoContext`/`ErrorContext`/
  `WarnContext` call sites — this codebase uses ctx-less `logger.Info`/
  `logger.Error` everywhere. A Handler-based rewrite would mean
  switching every call site to the `*Context` variants — a broad
  refactor, not what this spec is scoped to do.
- Instead, extend `middleware.RequestLog`'s **already-established**
  pattern: it already manually builds a `[]any` of `slog.Attr`s right
  before its one `logger.Info` call, pulling `request_id` via
  `RequestIDFromContext`. Adding `trace_id`/`span_id` the same way,
  via `trace.SpanContextFromContext(ctx)` (`go.opentelemetry.io/otel/trace`,
  already a dependency), is the minimal-diff option and reuses a
  pattern this codebase already chose once for the identical problem.
- Confirmed by tracing directly through the actual code (not assumed):
  `otelhttp.NewMiddleware("race-service")` is the **outermost** layer
  in `cmd/server/run.go`'s chain
  (`otelhttp(...)(RequestID()(RequestLog(logger)(Cors(...)(server))))`),
  so by the time `RequestLog`'s handler runs, `r.Context()` already
  carries both the span `otelhttp` started and the `request_id`
  `RequestID` added — no reordering needed, `RequestLog` just needs to
  read what's already there.

## Plan

1. **Shared helper, decided (was the open question, now resolved)**:
   new `internal/tracing/logattrs.go`, adding one small function to the
   package Distributed Tracing — Instrumentation already created —
   avoids triplicating the identical `SpanContextFromContext`/`IsValid`/
   `slog.String` pattern across `requestlog.go`, `wsgateway/endpoint.go`,
   and `consumer`'s two loop files. `internal/tracing` has no
   `internal/room`-style layering concern to protect (nothing importing
   it needs to stay decoupled from OpenTelemetry — the whole package
   exists to be the OTel seam), so this is a clean, unforced home, not
   a new package:

   ```go
   // internal/tracing/logattrs.go
   package tracing

   import (
       "context"
       "log/slog"

       "go.opentelemetry.io/otel/trace"
   )

   // LogAttrs returns trace_id/span_id slog attributes for ctx's active
   // span (logging/log-trace-correlation.md) — nil if ctx carries no
   // valid span context, so callers can unconditionally
   // append(fields, tracing.LogAttrs(ctx)...) the same way they already
   // conditionally append request_id/user_id.
   func LogAttrs(ctx context.Context) []any {
       sc := trace.SpanContextFromContext(ctx)
       if !sc.IsValid() {
           return nil
       }
       return []any{
           slog.String("trace_id", sc.TraceID().String()),
           slog.String("span_id", sc.SpanID().String()),
       }
   }
   ```

   Every call site below becomes `fields = append(fields,
   tracing.LogAttrs(ctx)...)` instead of the spec's inline 4-line
   block — same outcome, one implementation. `internal/middleware` and
   `internal/consumer` both gain a new import on `internal/tracing`;
   checked for cycles — `internal/tracing` imports only `go.opentelemetry.io/*`,
   nothing internal, so both directions are safe.
2. **REST** (`internal/middleware/requestlog.go`): right next to the
   existing `RequestIDFromContext` check, add
   `fields = append(fields, tracing.LogAttrs(r.Context())...)`.
3. **`ws-gateway`** (`internal/wsgateway/endpoint.go`): a real
   structural gap found while planning, not mentioned in the spec's
   own sketch — `readLoop`'s `logger.Info("wsgateway: published", ...)`
   line (called right after `publishInboundTraced`) currently has no
   way to see the `ws.frame` span `publishInboundTraced` creates
   internally, since that helper only returns `error` today. Fix:
   change `publishInboundTraced`'s signature to also return the span's
   `context.Context` (`func publishInboundTraced(...) (context.Context, error)`),
   so `readLoop` can pass it straight into `tracing.LogAttrs` for both
   the `"wsgateway: published"` success line and the
   `"wsgateway: publish message failed"`/`"wsgateway: publish
   disconnected failed"` error lines.
4. **`consumer`**:
   - `race_finished_loop.go`'s `processRaceFinishedMessage` already
     shadows its `ctx` parameter with the `kafka.consume` span's
     context (`ctx, span := tracer.Start(...)`) — every `c.logger.Warn`/
     `c.logger.Error` call inside it already has that `ctx` in scope,
     so each just gains `tracing.LogAttrs(ctx)...` appended to its
     fields.
   - `workout_sample_loop.go`'s `runWorkoutSampleLoop` currently
     **discards** the `kafka.consume` span's context
     (`_, span := tracer.Start(msgCtx, "kafka.consume", ...)`), so
     `"dropping malformed workout sample"` currently has no path to a
     `trace_id` even though a span exists for that exact message. Fix:
     capture it (`spanCtx, span := tracer.Start(...)`) and use `spanCtx`
     for that one Warn/Error pair.
   - **Deliberately out of scope, and staying that way**: `workout_sample_loop.go`'s
     `flushWorkoutSampleBatch` log lines (`"insert workout sample batch
     failed"`, etc.) do **not** get `trace_id` enrichment — a flush
     writes many messages' worth of samples, each with its own
     independent `kafka.consume` span, in one call. There is no single
     correct `trace_id` for a batch-level log line, the same
     many-to-one reasoning Distributed Tracing — Instrumentation's own
     Plan already established for why the ingest trace doesn't
     continue through `RoomActor`'s ticker-driven broadcast either.
5. **Resolving the spec's open question** (`internal/room`/`internal/race`
   per-tick/per-event logging): answered by what's already built, not
   re-opened here. `RoomActor`'s own log lines (`"publish workout sample
   failed"`, `"finish race failed"`, etc.) run against `r.ctx` — the
   room's own long-lived context, never a per-event one, by Distributed
   Tracing — Instrumentation's own deliberate, documented scoping
   decision (`RoomEvent`/`RoomActor.inbox` carry no per-event context
   today). Since there is no per-event span context reaching
   `RoomActor` at all, there is nothing for this feature to enrich
   there — correctly out of scope, not a gap this feature should try
   to close by itself (closing it would mean redoing the invasive
   context-threading refactor Distributed Tracing — Instrumentation
   explicitly declined to do).
6. Verification: `go build ./...`, `go test -race ./...` across
   `internal/middleware`, `internal/wsgateway`, `internal/consumer` —
   the packages this feature actually touches.

No divergence from `context/project-overview.md` — this is a direct
implementation of §9's log/trace correlation intent, not a design
change.

## Notes

- **Hard dependency, already satisfied**: depends on Distributed
  Tracing — Instrumentation, which is already merged to `master` — the
  `trace_id`/`span_id` this feature reads all come from spans that
  feature's `otelhttp`/`ws.frame`/`kafka.consume` instrumentation
  already produces.
- Doesn't touch `logging/efk-deploy.md`'s ingestion path — Fluent Bit
  ships whatever JSON `slog` already emits; new fields are invisible to
  it, no parser change needed.
- Doesn't add `span.AddEvent(...)` — logs and traces stay two separate
  systems correlated by a shared id, matching `phase-6-plan.md`'s
  architecture (logs through Fluent Bit, traces through the Collector,
  deliberately not the same pipeline).
- `logging/efk-deploy.md` has no hard dependency on this spec beyond
  wanting `trace_id` already flowing before the correlation view is
  worth demoing in Kibana — the two can build in either order.
- Grafana's own "correlate logs and traces" pivot
  (`dashboards/grafana-deploy.md`) is what actually surfaces this in
  the UI — this spec only makes the data exist.
- `trace.SpanContextFromContext` is a pure, stateless read off
  `context.Context` — no new synchronization anywhere this spec
  touches, same as `RequestIDFromContext`'s existing pattern.
