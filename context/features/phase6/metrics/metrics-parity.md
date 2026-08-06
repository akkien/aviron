# Metrics Parity — `ws-gateway` + `consumer`

## Overview

`race-service` is the only one of this project's three binaries with any
Prometheus wiring today — confirmed by grepping for
`prometheus/client_golang`, used in exactly one place, `internal/metrics`.
`internal/metrics.go`'s own doc comment records why: a connection-count
gauge existed once, wired against `internal/ws.WSHandler`, and was
**removed** when `room-service-adapter.md` moved connection-holding to
`internal/wsgateway` — the comment says rebuilding it is `ws-gateway.md`'s
job, which never happened. `ws-gateway`/`consumer` have neither `/metrics`
nor `/debug/pprof/*` today.

Per `phase-6-plan.md`'s Decisions #6, this isn't just "add the default
`client_golang` process collectors to both binaries." Both sit directly on
top of the cross-process infrastructure this system actually depends on
(NATS, Redis, Kafka), and that's exactly what needs its own gauges,
counters, and histograms — generic process metrics alone wouldn't tell you
anything about the hop that's actually slow.

## Requirements

### Where the code lives

`internal/metrics.Metrics` (`race-service`'s type) is tightly coupled to
`*room.Registry` — `RegisterRoomGauges(registry *room.Registry)` — so it
isn't directly reusable as-is. Two new constructors go in the same
`internal/metrics` package, alongside the existing one, so `promhttp`
wiring (`prometheus.NewRegistry()` + `collectors.NewGoCollector()` +
`collectors.NewProcessCollector()` + `Handler()`) isn't triplicated:

```go
// internal/metrics (existing package)
func NewGatewayMetrics() *GatewayMetrics
func NewConsumerMetrics() *ConsumerMetrics
```

Each returns its own small type with a `Handler() http.Handler`, same
shape as the existing `Metrics.Handler()`.

### `GET /metrics` + `/debug/pprof/*` wiring

Both binaries wire these the same way `internal/httpserver/route.go`
already does for `race-service` — `server.Handle("GET /metrics",
m.Handler())` and the same explicit `pprof.Index`/`Cmdline`/`Profile`/
`Symbol`/`Trace` registrations (not a blank `import _ "net/http/pprof"`,
which would silently attach to `http.DefaultServeMux` instead of the
binary's own mux):

- `cmd/ws-gateway/run.go` — added directly to its `mux :=
  http.NewServeMux()`, unauthenticated and uncors'd like `race-service`'s
  (a scraper carries no JWT, is never called from a browser). Gated behind
  the same `PPROF_ENABLED` `ConfigMap` key `race-service` already reads,
  for consistency — `internal/wsgateway.Config` gains the field.
- `cmd/consumer/run.go` — this binary has no HTTP server at all today (no
  `Service` in `deploy/k8s/consumer/deployment.yaml`, confirmed by
  `docs/k8s-deployment.md`'s own "no Service" note). A minimal
  `http.Server` needs to exist purely to serve `/metrics` and
  `/debug/pprof/*` — small, but a real addition: a `Service` for
  `consumer` becomes a prerequisite for `metrics/prometheus-deploy.md` to
  actually scrape it.

### `ws-gateway`'s metrics

- `aviron_ws_connections_active` (`GaugeFunc`) — the rebuild `internal/
  metrics.go`'s own comment flagged as owed. Source: `raceHubRegistry`
  (`internal/wsgateway/racehub.go`) tracks connections per `raceHub` in a
  `map[chan []byte]context.CancelFunc`, but nothing today sums across
  every `raceHub` a registry holds. **Open question for `start`**: add a
  `Count() int` to `raceHubRegistry` (mirrors `room.Registry.Count()`'s
  existing shape exactly) that sums each hub's connection-map length —
  needs a query-through-channel or an `atomic.Int64` threaded through
  `raceHub.run`'s single-writer loop, the same design choice
  `prometheus-metrics.md` left open for the original (removed) gauge.
- `aviron_roomrelay_publish_total{subject_kind}` /
  `aviron_roomrelay_publish_errors_total{subject_kind}` /
  `aviron_roomrelay_publish_duration_seconds{subject_kind}` — added at
  `internal/roomrelay.Bus.PublishIn`/`PublishOut`, not duplicated
  per-binary, since `Bus` is shared code both `ws-gateway` and
  `race-service` call into (`ws-gateway` publishes on `in`, `race-service`
  publishes on `out`) — instrumenting the shared package once means both
  binaries get the metric automatically. `subject_kind` is `"in"` or
  `"out"`, never `race_id` — the same cardinality discipline
  `prometheus-metrics.md`'s own Notes already established ("don't label by
  `race_id`").
- `aviron_roomlocator_lookup_duration_seconds{op,outcome}` /
  `aviron_roomlocator_errors_total{op}` — added at `internal/
  roomlocator.Locator`'s methods (`Owner`, `Claim`, `Refresh`, `Release`,
  `MarkEvicted`, `IsEvicted`), same "instrument the shared package once"
  reasoning as `roomrelay` above — `race-service` calls the write-side
  methods, `ws-gateway` calls `Owner`/`IsEvicted`/`SubscribeRoomEvents`,
  both benefit from the same histogram/counter pair. `op` is the method
  name (`"owner"`, `"claim"`, ...), `outcome` is `"ok"`/`"error"`/(for
  `Owner`)`"not_found"`.

### `consumer`'s metrics

- `aviron_kafka_consumer_lag{topic}` (`GaugeFunc`) — `segmentio/kafka-go`'s
  `*kafkago.Reader.Stats()` already exposes a `Lag` field; `internal/
  consumer.Consumer.Run` currently constructs both readers as local
  variables inside their own goroutines (`consumer.go`), so they aren't
  reachable from outside. Needs a small structural change: store both
  `*kafkago.Reader`s on the `Consumer` struct itself (set once in `Run`,
  read from a new `Consumer.Lag() (workoutSample, raceFinished int64)`
  accessor) rather than keeping them fully local.
- `aviron_consumer_batch_insert_duration_seconds{topic}` /
  `aviron_consumer_batch_insert_errors_total{topic}` — observed around
  `sampleBatch`'s flush call site (`consumer.go`'s workout-sample loop)
  and `FinishReconciler.ReconcileParticipantResults`'s call site
  (race-finished loop) — the two places `WorkoutSampleWriter.InsertBatch`/
  `FinishReconciler` actually touch Postgres.
- `aviron_consumer_dlq_total{topic}` (`Counter`) — incremented every time
  `DLQPublisher.PublishRaw` is called (`ErrPermanentWrite` path) — a
  non-zero rate here is a real signal worth alerting on
  (`alerting/alert-rules.md`), not just informational.

## Concurrency

- `raceHubRegistry.Count()` (if built via query-through-channel, the
  option `prometheus-metrics.md` leaned away from for the simpler
  `atomic.Int64`) must not block a scrape behind `raceHub.run`'s own
  single-writer loop under load — same tradeoff already reasoned through
  once for the original connection gauge, reused here rather than
  re-litigated.
- `roomrelay`/`roomlocator` metric recording happens inline in already-
  concurrent-safe call sites (`Bus.nc.Publish` is safe for concurrent use;
  `Locator`'s methods are already independently safe per-call) —
  `prometheus.Counter`/`Histogram.Observe` are themselves safe for
  concurrent use, so no new locking.
- `Consumer.Lag()` reads `*kafkago.Reader.Stats()`, which `kafka-go`
  documents as safe to call concurrently with the reader's own fetch loop.

## Data

```go
// internal/metrics (new)
func NewGatewayMetrics() *GatewayMetrics
func (m *GatewayMetrics) Handler() http.Handler
func (m *GatewayMetrics) RegisterConnectionGauge(hubs *wsgateway.RaceHubRegistry)

func NewConsumerMetrics() *ConsumerMetrics
func (m *ConsumerMetrics) Handler() http.Handler
func (m *ConsumerMetrics) RegisterLagGauge(c *consumer.Consumer)

// internal/roomrelay (instrumented in place, not a new type)
func (b *Bus) PublishIn(ctx context.Context, raceID string, env InboundEnvelope) error  // gains metrics
func (b *Bus) PublishOut(ctx context.Context, raceID string, env OutboundEnvelope) error // gains metrics

// internal/roomlocator (instrumented in place)
func (l *Locator) Owner(ctx context.Context, raceID string) (string, bool, error) // gains metrics
// ... same for Claim/Refresh/Release/MarkEvicted/IsEvicted

// internal/consumer
type Consumer struct {
    // ...existing fields
    workoutReader, raceFinishedReader *kafkago.Reader // new: stored, not local
}
func (c *Consumer) Lag() (workoutSample, raceFinished int64)
```

## Notes

- `internal/roomrelay`/`internal/roomlocator` gaining a dependency on
  `prometheus/client_golang` is a new import in two packages that
  currently have zero HTTP/metrics awareness — worth a second look at
  `start` on whether that's the right seam, or whether metrics recording
  should instead wrap `Bus`/`Locator` from the outside (a decorator) to
  keep those packages free of `prometheus` imports, mirroring
  `prometheus-metrics.md`'s own concern about `internal/room`/`internal/
  ws` staying free of HTTP/metrics imports. Leaning toward instrumenting
  in place here since both types are already leaf infrastructure wrappers
  with no equivalent layering concern `room-actor-core.md` raised — confirm
  at `start`.
- `consumer` needing an actual `http.Server` (today it has none) also
  means `deploy/k8s/consumer/deployment.yaml` needs a `Service` added —
  flagged here, actually wired in `metrics/prometheus-deploy.md`.
- No dependency on any other Phase 6 spec — this is the one with nothing
  blocking it, per `phase-6-plan.md`'s "Dependency order".
