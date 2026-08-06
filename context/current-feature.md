# Current Feature: Distributed Tracing — Instrumentation

## Status

In Progress

## Goals

- OpenTelemetry Go SDK wired into all three binaries (`cmd/server`,
  `cmd/ws-gateway`, `cmd/consumer`) at the full depth `phase-6-plan.md`'s
  Decisions #3 settled on — not a partial/critical-events-only version.
- Every hop gets spans: REST entry points, WebSocket entry points
  (`internal/wsgateway`), NATS (`internal/roomrelay`), Redis
  (`internal/roomlocator`), Kafka (`internal/kafka`, `internal/consumer`),
  and `pgx` queries (all three domain repositories).
- Trace context propagates correctly across every non-HTTP process
  boundary (NATS message headers, Kafka message headers) so a publish
  span and its corresponding receive span land in the same trace.
- Each `telemetry` WebSocket frame produces one continuous end-to-end
  trace: `ws-gateway` receive -> NATS `in` publish -> NATS `in` receive
  -> `RoomActor.applyEvent` -> NATS `out` publish (broadcast) ->
  `ws-gateway` fan-out.
- New OTel dependencies added and `go mod tidy` run; spans exported via
  OTLP to the collector endpoint already deployed by
  `tracing/otel-collector-tempo-deploy.md`.
- Verify spans actually land in Tempo before calling this done — this
  spec depends on the collector/Tempo deployment already being live.

## Explain

- This is the spec that touches application code for tracing (as
  opposed to `otel-collector-tempo-deploy.md`, which only stood up the
  collector/Tempo manifests).
- The core design problem shared by every hop: propagating trace context
  across process boundaries that aren't HTTP. OTel's
  `propagation.TraceContext` (W3C `traceparent`) is transport-agnostic
  but needs a carrier — NATS and Kafka both support message headers that
  can hold one, but this project's own envelope types
  (`roomrelay.InboundEnvelope`/`OutboundEnvelope`,
  `workoutSamplePayload`/`raceFinishedPayload`) don't carry them today.
- **REST**: new `otelhttp`-style middleware added to the existing chain
  in `cmd/server/run.go`, extracting any inbound `traceparent` (none from
  a browser today, but the hook exists for future service-to-service
  callers) and injecting trace/span IDs into the request context that
  handlers already thread through.
- **WebSocket**: `internal/wsgateway.WSHandler`'s `GET /ws` upgrade gets
  one span for the connection join (`join_race`) and one span per
  decoded client frame — not one span for the whole connection lifetime.
- `middleware.RequestID`'s `request_id` stays distinct from the
  trace/span ID; both get attached to the same log line once
  `log-trace-correlation.md` lands.
- **NATS**: real code change, not just added spans. `Bus.PublishIn`/
  `PublishOut` currently call `nc.Publish(subject, payload)` — a plain
  API with no header support — and must switch to
  `nc.PublishMsg(&nats.Msg{...})` so a `nats.Header` can carry the
  injected trace context. `subscribe`'s receive side extracts the header
  into a fresh `context.Context` before decoding. New type:
  `natsHeaderCarrier` (`propagation.TextMapCarrier` backed by
  `nats.Header`). Two spans per hop: `roomrelay.publish` and
  `roomrelay.receive`, tagged `race_id`/`subject`.
- **Redis**: simpler — no cross-process context to propagate (request/
  response within one process). One span per method (`Owner`, `Claim`,
  `Refresh`, `Release`, `MarkEvicted`, `IsEvicted`,
  `SubscribeRoomEvents`), tagged `race_id`/`outcome`, at the same call
  sites `metrics-parity.md` already wraps for latency/error metrics.
- **Kafka**: `kafkago.Message` already has a `Headers` field, so no
  structural change like NATS needed. `Producer.PublishWorkoutSample`/
  `PublishRaceFinished` inject the current span into `Headers` before
  `WriteMessages`; `internal/consumer`'s two reader loops extract it
  per-message before decoding. Two spans per message: `kafka.produce`
  (topic, key = `race_id`) and `kafka.consume` (topic, `group_id`) —
  these gaps can be minutes wide (consumer lag), which the trace should
  make visible, not hide.
- **pgx**: use `github.com/exaring/otelpgx`'s `pgx.QueryTracer`
  implementation, wired once in `internal/db.NewPool` via
  `ConnConfig.Tracer` rather than hand-instrumenting every repository
  method. Every query gets a span automatically, tagged with the
  parameterized SQL statement (never literal values — extends this
  project's existing no-PII/secrets-in-logs discipline to spans).
- **Per-telemetry-message spans**: the decision already made in
  `phase-6-plan.md`'s Decisions #3. Volume is a real, disclosed concern
  here (unlike elsewhere in this spec) — roughly one message per
  0.4-2s per player, so a 10-player race at full throttle is on the
  order of 5-25 spans/sec across the whole pipeline. Expected to be fine
  on a laptop `kind` cluster, but worth re-checking against real numbers
  once `verification/phase-6-verification.md` runs an actual multi-race
  load test.
- **Concurrency**: `Inject`/`Extract` are documented safe for concurrent
  use; many-goroutines-one-`Tracer` (the `RoomActor` broadcast path) is
  exactly what the OTel Go SDK is designed for — no new locking
  anywhere this spec touches.

## Plan

- Sequence work hop by hop, verifying each lands in Tempo before moving
  to the next, since all of this depends on
  `tracing/otel-collector-tempo-deploy.md` already being live:
  1. Add core OTel deps (`go.opentelemetry.io/otel`, `.../otel/sdk`,
     `.../otel/exporters/otlp/otlptrace/otlptracegrpc`) and a shared
     tracer-provider bootstrap, likely `internal/tracing` (new package,
     mirroring how `internal/config` centralizes env access) —
     initialized once per binary in each `cmd/*/run.go`, pointed at the
     OTLP endpoint via an `internal/config` field (consistent with the
     "no bare `os.Getenv`" convention), defaulting to
     `otel-collector.aviron.svc.cluster.local:4317`.
  2. REST entry points: add `otelhttp` middleware to `cmd/server/run.go`
     alongside the existing `middleware.RequestID`/`middleware.
     RequestLog` chain.
  3. WebSocket entry points: instrument `internal/wsgateway.WSHandler`'s
     `GET /ws` — one span for `join_race`, one per decoded frame.
  4. NATS: change `Bus.PublishIn`/`PublishOut` to `nc.PublishMsg`, add
     `natsHeaderCarrier`, inject on publish / extract on subscribe in
     `internal/roomrelay`.
  5. Redis: wrap the existing `internal/roomlocator` methods with spans
     at the same call sites `metrics-parity.md` touches — coordinate so
     the two diffs don't collide on the same functions.
  6. Kafka: inject/extract headers in `internal/kafka`'s
     `Producer.PublishWorkoutSample`/`PublishRaceFinished` and
     `internal/consumer`'s two reader loops.
  7. pgx: add `github.com/exaring/otelpgx`, wire `otelpgx.NewTracer()`
     into `internal/db.NewPool`'s `ConnConfig.Tracer`.
  8. Wire the full per-telemetry-message trace end-to-end and confirm
     one continuous trace shows up in Tempo spanning `ws-gateway` ->
     NATS `in` -> `race-service` -> NATS `out` -> `ws-gateway`.
  9. `go mod tidy`, `go build ./...`, `go test -race ./...` across every
     touched concurrency-relevant package
     (`internal/roomrelay`, `internal/roomlocator`, `internal/wsgateway`,
     `internal/consumer`).
- No divergence from `context/project-overview.md` — this spec is an
  implementation of §9's "Phase 6 builds full-depth distributed
  tracing" commitment, not a design change.
- **One deliberate scoping decision, made during implementation and
  worth flagging**: the spec's "per-telemetry-message spans" section
  describes one continuous trace spanning all the way through
  `ws-gateway`'s broadcast fan-out. Implemented instead: the trace
  covers ingest only — `ws-gateway` frame receive (`ws.frame`) ->
  `roomrelay.publish` (NATS `in`) -> `roomrelay.receive` (NATS `in`,
  race-service side) — and ends there. It does **not** continue into
  `RoomActor.applyEvent`, nor into the eventual `roomrelay.publish`
  (NATS `out`)/fan-out. Reason: `RoomEvent`/`RoomActor.inbox`/
  `RoomActor.broadcast` carry no context today, and `broadcastSnapshot`
  is a periodic, ticker-driven aggregate of every participant's state
  (`room.go`'s `Run` ticks every 250ms independent of any single
  `TelemetryReceived`) — there is no correct 1:1 mapping from one
  input message to one broadcast output to hang a single trace off of;
  forcing one would misrepresent causality, not just add plumbing.
  Threading a context through the actor's event/broadcast channels to
  fix this would be a much larger, more invasive change than anything
  else this spec touches (unlike the NATS header change, it isn't
  hinted at in the spec's own "Data" section's list of new
  types/functions). The broadcast leg still gets its own spans
  (`roomrelay.publish`/`roomrelay.receive` on the `out` subject), just
  as an independent trace tied to the tick, not literally chained to
  whichever message happened to arrive most recently.
- Coordinate with `metrics/metrics-parity.md` on the `roomrelay`/
  `roomlocator` call sites (both specs touch the same functions) to
  avoid two diffs stepping on each other, though there's no hard
  ordering requirement between them.

## Notes

- New dependencies (none currently in `go.mod`): `go.opentelemetry.io/
  otel`, `go.opentelemetry.io/otel/sdk`, `go.opentelemetry.io/otel/
  exporters/otlp/otlptrace/otlptracegrpc`, `go.opentelemetry.io/contrib/
  instrumentation/net/http/otelhttp`, `github.com/exaring/otelpgx` (or
  equivalent) — run `go mod tidy` after adding.
- Depends on `tracing/otel-collector-tempo-deploy.md` being live already
  (need a real OTLP endpoint to verify spans land).
- Benefits from, but doesn't strictly require, doing this close to
  `metrics/metrics-parity.md` since both touch the same `roomrelay`/
  `roomlocator` call sites.
- `logging/log-trace-correlation.md` is the next spec and depends on
  this one directly — it can't inject a `trace_id` into `slog` output
  before a trace/span actually exists to read one from.
- No server-side inspection of typed text (per project-overview.md §13)
  is affected by this spec — tracing is purely infrastructure, not a
  changed trust model.
- **Live-verified against the already-running kind cluster**, not just
  `go build`/`go test -race`: rebuilt `aviron-backend:local`, `kind
  load docker-image`d it, rolled out `race-service`/`ws-gateway`/
  `consumer`, then drove a real register -> login -> create race ->
  join -> start -> WebSocket telemetry flow through the `ws-gateway`
  ingress and queried Tempo's `/api/search`/`/api/traces` directly.
  Confirmed: REST spans with parameterized-SQL `pgx` child spans
  (`pool.acquire`/`prepare`/`query`, no literal values); `ws.join_race`
  with `roomlocator.owner`/`roomlocator.is_evicted` Redis children;
  `ws.frame` -> `roomrelay.publish` (`ws-gateway`) -> `roomrelay.receive`
  (`race-service`) landing in one trace across the NATS hop; and
  `kafka.produce` (`race-service`) -> `kafka.consume` (`consumer`)
  landing in one trace across the Kafka hop. This is the concrete
  evidence behind the Goals section's "verify spans actually land in
  Tempo" item.
