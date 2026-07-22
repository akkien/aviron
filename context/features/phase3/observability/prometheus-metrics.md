# Prometheus Metrics

## Overview

`context/project-overview.md` §9 names 5 specific metrics: **active room
count, connection count, broadcast tick latency, goroutine count, channel
buffer usage** — "direct visibility into goroutine/memory leaks — right in
line with the JD." This spec adds a `GET /metrics` endpoint
(`github.com/prometheus/client_golang`, a new dependency — not in `go.mod`
today, confirmed by direct grep) exposing exactly those 5, plus the
standard Go process/runtime collectors that library registers for free.

This is the spec `load-testing/k6-load-test.md` exists to put load behind —
a load test without these metrics running underneath it can only observe
external symptoms (latency, dropped connections), not *why* — this spec is
what turns "the room actor got slow" into "tick latency p99 crossed 200ms
right as goroutine count crossed 5,000."

## Requirements

### Endpoint

- `GET /metrics` — the standard path every Prometheus scrape config
  expects by default. Registered directly on the mux like `GET /healthz`
  (`internal/httpserver/route.go`), **not** wrapped in `requireAuth` (a
  scraper doesn't carry a user JWT) and **not** wrapped in `middleware.Cors`
  either, since it's never called from a browser.
- Uses `promhttp.Handler()` from `client_golang/prometheus/promhttp` —
  no hand-rolled text-format encoding.

### The 5 metrics

1. **Active room count** — `room.Registry` already holds
   `map[string]*RoomActor` behind a `sync.RWMutex`
   (`internal/room/registry.go`). Exposed as a `prometheus.GaugeFunc`
   (computed at scrape time, not polled on a timer) wrapping a new small
   exported method, e.g. `Registry.Count() int` — a one-line addition, the
   mutex already makes this safe to call from the scrape handler's
   goroutine.
2. **Connection count** — `internal/ws`'s `hub` tracks connections in a map
   that's only ever touched by `hub.run()`'s own goroutine (single-writer,
   per `docs/concurrency.md`). Two options, **open question for
   `load`/`start`**:
   - (a) a package-level `atomic.Int64`, incremented/decremented right
     next to `hub.registerConn`/`unregisterConn`'s existing map
     mutations — simplest, and `atomic` reads are safe from the
     Prometheus scrape goroutine with no further changes to `hub`'s
     single-writer design.
   - (b) a query-through-the-existing-channel pattern, mirroring
     `RoomActor`'s `evictionQuery`/reply-channel shape
     (`internal/room/room.go`) — more "pure" single-writer, more
     plumbing for a metric that doesn't need per-scrape precision.
   - Leaning toward (a): the count only needs to be *eventually*
     accurate for a gauge scraped every 10-15s, and `docs/concurrency.md`'s
     single-writer principle exists to protect *state that drives
     behavior* (who's connected, what they see) — a metric counter reading
     doesn't feed back into any decision the hub makes, so a plain atomic
     is proportionate, not a shortcut around the documented guarantee.
   - There's one connection-count gauge globally, and (likely) a per-room
     breakdown isn't needed for a side project — confirm at `start`
     whether per-room labels are worth the cardinality (num rooms × 1
     label value each) or overkill.
3. **Broadcast tick latency** — a `prometheus.Histogram`, observed inside
   `RoomActor.Run()`'s `case <-ticker.C:` branch
   (`internal/room/room.go`), timing `broadcastSnapshot()`'s own
   execution (marshal + non-blocking send) — not the interval *between*
   ticks (that's `time.Ticker`'s own job to keep steady; what's
   interesting operationally is whether the work done *inside* each tick
   is creeping up as room/participant count grows).
4. **Goroutine count** — `runtime.NumGoroutine()`, wrapped in a
   `prometheus.GaugeFunc`. No design decision needed; this is the simplest
   of the 5.
5. **Channel buffer usage** — three distinct buffered channels exist
   today, each a separate gauge (or one gauge with a `channel` label):
   `RoomActor.inbox` (cap 64), `RoomActor.broadcast` (cap
   `broadcastBufferSize` = 16), and each WS connection's own `connCh`
   (cap `connBufferSize` = 8, per-connection — likely needs summing or a
   max-across-connections reduction rather than one gauge per connection,
   to avoid unbounded cardinality as connection count grows). `len(ch)` is
   documented-safe to call from any goroutine, so no synchronization
   changes needed to read these — the only real design question, **for
   `load`/`start`**, is how to reach a `RoomActor`'s/connection's channels
   from the metrics-registration code without breaking the
   `internal/room`/`internal/ws` packages' existing "no HTTP/metrics
   imports" layering (`room-actor-core.md`'s Notes section: "this package
   has zero HTTP/WebSocket imports... keeps `go test -race` on this
   package fast and focused"). Likely answer: metrics collection lives in
   `internal/room`/`internal/ws` themselves (a `Registry.CollectMetrics()`-
   style method returning plain numbers), and only the `prometheus`
   wiring/registration lives in a new top-level package — confirm the
   exact seam at `start`.

### Naming convention

- Prefix every metric `aviron_` (e.g. `aviron_rooms_active`,
  `aviron_connections_active`, `aviron_tick_latency_seconds`,
  `aviron_goroutines`, `aviron_channel_buffer_used`) — Prometheus's own
  naming convention (`<namespace>_<subsystem>_<name>_<unit>`), and
  distinguishes this app's metrics from the Go runtime collectors
  `client_golang` registers automatically (`go_goroutines`,
  `go_memstats_*`, etc. — which cover "goroutine count" for free, worth
  confirming at `start` whether metric #4 above is even worth a duplicate
  custom gauge or whether the auto-registered `go_goroutines` already
  satisfies §9's requirement).

## Concurrency

- Every metric read here is either already-synchronized (Registry's
  mutex), a documented-safe builtin (`len(ch)`, `runtime.NumGoroutine()`),
  or a plain atomic counter — no new locks, no new goroutines beyond
  whatever `promhttp.Handler()` itself needs per scrape.
- The tick-latency histogram's `Observe()` call happens on `RoomActor`'s
  own single goroutine (inside `Run()`), so it never contends with
  anything else touching that actor's state — `prometheus.Histogram` is
  itself safe for concurrent `Observe()` calls from many different rooms'
  goroutines sharing the same histogram, which is exactly this metric's
  shape (one histogram, many rooms feeding it).

## Data

```go
// internal/room/registry.go
func (reg *Registry) Count() int

// internal/metrics (new package, name TBD at start)
func NewMetrics() *Metrics // registers all 5 (+ auto Go collectors) against
                             // a prometheus.Registry, returns handles for
                             // the room/ws packages to call into (e.g.
                             // metrics.ObserveTickLatency(d time.Duration))
```

## Notes

- New dependency: `github.com/prometheus/client_golang` — first
  third-party dependency added specifically for Phase 3, run `go mod tidy`
  after.
- Depends on `structured-logging.md` only loosely (no hard technical
  dependency, sequenced after it per that spec's own Notes section).
- `load-testing/k6-load-test.md` is this spec's actual consumer — load
  testing without these metrics running is still possible, just far less
  useful for finding *why* something got slow, not just *that* it did.
- Per-room or per-connection labels are a real cardinality risk or a
  needed level of detail. Default to *not* labeling by `race_id` unless a
  concrete debugging need shows up during load testing — a scraped label
  set that grows with every race ever created is the kind of Prometheus
  mistake this spec should actively avoid, not discover the hard way.
