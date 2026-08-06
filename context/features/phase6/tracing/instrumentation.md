# Distributed Tracing — Instrumentation

## Overview

Wires the OpenTelemetry Go SDK into all three binaries, at the full depth
`phase-6-plan.md`'s Decisions #3 settled on: REST/WebSocket entry points,
NATS (`internal/roomrelay`), Redis room-ownership lookups (`internal/
roomlocator`), Kafka (`internal/kafka`, `internal/consumer`), and `pgx`
query spans — including **a span per `telemetry` message**, not just
critical events. Depends on `tracing/otel-collector-tempo-deploy.md`
already being live to verify against; this is the spec that actually
touches application code, not manifests.

The one substantive design problem every hop below shares: **propagating
trace context across a process boundary that isn't HTTP**. OpenTelemetry's
`propagation.TraceContext` (W3C `traceparent` format) is transport-
agnostic — it needs a carrier to read/write on, and NATS/Kafka both
support message headers that can hold one, but this project's own wire
types (`roomrelay.InboundEnvelope`/`OutboundEnvelope`,
`workoutSamplePayload`/`raceFinishedPayload`) don't carry them today.

## Requirements

### REST + WebSocket entry points

- REST: a new `otelhttp`-style middleware in the existing chain
  (`middleware.RequestID()(middleware.RequestLog(logger)(...))`,
  `cmd/server/run.go`) starts a span per request, extracting any inbound
  `traceparent` header (there won't be one from a browser client, but the
  hook exists for a future service-to-service caller) and injecting the
  resulting trace/span ID into the request's `context.Context` — the same
  context every handler already threads through
  service/repository/actor calls.
- WebSocket: `internal/wsgateway.WSHandler`'s `GET /ws` upgrade starts one
  span for the connection's join itself (`join_race`), then one span per
  decoded client frame (see "Per-telemetry-message spans" below) — not
  one span for the connection's entire lifetime, which doesn't fit
  OpenTelemetry's request/response span shape any better than it did in
  `phase3`'s original, narrower tracing spec.
- `middleware.RequestID`'s own `request_id` stays a separate identifier
  from the trace/span ID — not merged — but both get attached to the
  same log line once `logging/log-trace-correlation.md` lands, so a human
  can pivot from either.

### NATS (`internal/roomrelay`)

**Real code change, not just added spans**: `Bus.PublishIn`/`PublishOut`
call `nc.Publish(subject, payload)` today — a plain `(subject, []byte)`
API with no header support. Trace context propagation needs NATS message
headers (`nats.Msg.Header`, supported since NATS 2.2, already true of
this project's `nats:2-alpine`/`nats-server/v2 v2.14.3`), so both
functions switch to `nc.PublishMsg(&nats.Msg{...})`:

```go
// internal/roomrelay/bus.go — shape of the change
func publish[T any](ctx context.Context, nc *nats.Conn, subject string, env T) error {
    payload, err := json.Marshal(env)
    // ...
    header := make(nats.Header)
    otel.GetTextMapPropagator().Inject(ctx, natsHeaderCarrier(header))
    return nc.PublishMsg(&nats.Msg{Subject: subject, Data: payload, Header: header})
}
```

`subscribe`'s receive side extracts the header back into a fresh `context.
Context` before decoding the envelope, so a span opened for
`ws-gateway`'s publish and a span opened for `race-service`'s corresponding
receive land in the same trace. A small `natsHeaderCarrier` adapter
(`propagation.TextMapCarrier` backed by `nats.Header`, which is a
`map[string][]string` like `http.Header`) is the only new type needed.

Two spans per hop: `roomrelay.publish` (client side) and
`roomrelay.receive` (subscriber side), both tagged `race_id`/`subject`.

### Redis (`internal/roomlocator`)

Simpler than NATS — no cross-process context to propagate (a Redis call
is a request/response within one process, not a message handed to
another process later), just a span per method (`Owner`, `Claim`,
`Refresh`, `Release`, `MarkEvicted`, `IsEvicted`, `SubscribeRoomEvents`),
tagged `race_id` and `outcome`. Same call sites `metrics/metrics-
parity.md` already instruments for latency/error metrics — the two
concerns (a span for tracing, an `Observe`/`Inc` for metrics) coexist at
the same wrapping point, not duplicated wrapping.

### Kafka (`internal/kafka`, `internal/consumer`)

`segmentio/kafka-go`'s `kafkago.Message` already has a `Headers []kafka.
Header` field — no structural change needed here, unlike NATS.
`Producer.PublishWorkoutSample`/`PublishRaceFinished` inject the current
span's context into `Headers` before `WriteMessages`; `internal/
consumer`'s two reader loops extract it back out per-message before
decoding, so a `race-service`-side publish span and a `consumer`-side
consume span land in the same trace, potentially minutes apart (Kafka
consumer lag is exactly the kind of gap a trace should make visible, not
hide).

Two spans per message: `kafka.produce` (topic, key = `race_id`) and
`kafka.consume` (topic, `group_id`) — plus the existing
`aviron_kafka_consumer_lag` metric (`metrics/metrics-parity.md`) gives
the aggregate view a trace's per-message view can't.

### `pgx` (all three domain repositories)

`jackc/pgx/v5` has a first-class `pgx.QueryTracer` hook
(`pgxpool.Config`'s `ConnConfig.Tracer`) — rather than hand-instrumenting
every repository method, use an existing community tracer
(`github.com/exaring/otelpgx`, widely used, wraps exactly this
interface) wired once in `internal/db.NewPool`, not per-repository. Every
`race`/`auth`/`leaderboard`/`postgres.WorkoutSampleRepository` query gets
a span automatically, tagged with the SQL statement (parameterized, not
literal values — this project's existing "no PII/secrets in logs"
discipline extends to spans).

### Per-telemetry-message spans

The decision already made in `phase-6-plan.md`'s Decisions #3: each
`telemetry` WebSocket frame gets one end-to-end trace spanning
`ws-gateway` receive -> `roomrelay.publish` (NATS `in`) ->
`roomrelay.receive` -> `race-service` `RoomActor.applyEvent` ->
`roomrelay.publish` (NATS `out`, the broadcast) -> `ws-gateway`'s
`raceHub` fan-out. This is the one place volume is a real, disclosed
concern rather than a settled non-issue: per `project-overview.md` §13
each player sends roughly one message per 0.4-2s, so a single race with
10 players at full throttle is on the order of 5-25 spans/sec across the
whole trace pipeline — comfortably inside what a local Tempo/Collector
pair on a laptop `kind` cluster handles, but worth re-checking against
real numbers once `verification/phase-6-verification.md` runs a genuine
multi-race load test, not assumed correct from this napkin math alone.

## Concurrency

- `otel.GetTextMapPropagator().Inject`/`Extract` are stateless,
  documented safe for concurrent use — no new locking anywhere this spec
  touches.
- Span creation from many goroutines sharing one `Tracer` (the `RoomActor`
  broadcast path, many rooms' goroutines) is exactly what the OTel Go SDK
  is designed for, same conclusion `phase3/observability/opentelemetry-
  tracing.md` already reached for this project's much smaller original
  scope.
- `natsHeaderCarrier`/Kafka header injection happen inline at existing
  publish call sites already safe for concurrent use (`Bus.nc.Publish` is
  concurrency-safe by NATS's own design; `kafkago.Writer.WriteMessages`
  likewise) — no new synchronization introduced.

## Data

```go
// internal/roomrelay (new)
type natsHeaderCarrier nats.Header
func (c natsHeaderCarrier) Get(key string) string
func (c natsHeaderCarrier) Set(key, value string)
func (c natsHeaderCarrier) Keys() []string

// internal/db (existing NewPool, gains a Tracer)
func NewPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) // internally sets ConnConfig.Tracer = otelpgx.NewTracer()
```

## Notes

- New dependencies: `go.opentelemetry.io/otel`, `go.opentelemetry.io/
  otel/sdk`, `go.opentelemetry.io/otel/exporters/otlp/otlptrace/
  otlptracegrpc`, `go.opentelemetry.io/contrib/instrumentation/net/http/
  otelhttp` (REST), `github.com/exaring/otelpgx` (or equivalent) — none
  currently in `go.mod`, run `go mod tidy` after.
- `OTEL_EXPORTER_OTLP_ENDPOINT` (or an explicit `internal/config` field,
  consistent with this project's existing "no bare `os.Getenv` outside
  `internal/config`/`internal/wsgateway.Config`" convention) points every
  binary at `otel-collector.aviron.svc.cluster.local:4317`.
- Depends on `tracing/otel-collector-tempo-deploy.md` (needs a real OTLP
  endpoint to verify spans actually land) and benefits from `metrics/
  metrics-parity.md` landing first only in that both touch the same
  `roomrelay`/`roomlocator` call sites — not a hard ordering requirement,
  just worth doing them close together to avoid two separate diffs
  touching the same functions.
- `logging/log-trace-correlation.md` is the next spec, and depends on
  this one directly — it can't inject a `trace_id` into `slog` output
  before a trace/span actually exists to read one from.
