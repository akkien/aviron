package metrics

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/akkien/aviron/internal/room"
)

// Metrics owns a private prometheus.Registry (not the global
// prometheus.DefaultRegisterer) so this package stays explicit and
// constructor-threaded rather than relying on mutable package-level state,
// consistent with this project's existing no-hidden-globals convention.
//
// Construction is split across NewMetrics and RegisterRoomGauges rather than
// one shot because of a real ordering constraint: room.Registry needs a
// room.TickObserver (this type) at its own construction time, but the room
// gauges below need a *room.Registry to read from — that doesn't exist
// until after Registry is constructed. NewMetrics has zero dependencies so
// it can run first; RegisterRoomGauges runs once its argument actually
// exists.
//
// Connection-count/conn-buffer-usage gauges (project-overview.md §9)
// previously lived here as RegisterWSGauges, wired against
// internal/ws.WSHandler — removed when room-service-adapter.md relocated
// that connection-holding code to internal/wsgateway, since race-service no
// longer holds any WebSocket connections to report on. ws-gateway.md owns
// bringing an equivalent gauge back, scoped to whatever process actually
// holds those connections next.
type Metrics struct {
	reg         *prometheus.Registry
	tickLatency prometheus.Histogram
}

// NewMetrics constructs the metrics registry, the standard Go
// process/runtime collectors (go_goroutines, go_memstats_*,
// process_cpu_seconds_total, etc — these already satisfy
// project-overview.md §9's "goroutine count" on their own, so no separate
// custom gauge duplicates runtime.NumGoroutine()), and the tick-latency
// histogram (the one metric observed from inside internal/room itself,
// rather than pulled at scrape time).
func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	tickLatency := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name: "aviron_tick_latency_seconds",
		Help: "Duration of a single room actor broadcast tick (marshal + non-blocking send).",
	})
	reg.MustRegister(tickLatency)

	return &Metrics{reg: reg, tickLatency: tickLatency}
}

// ObserveTick satisfies room.TickObserver. Safe for concurrent calls from
// many rooms' own goroutines sharing this one histogram, by prometheus
// design.
func (m *Metrics) ObserveTick(d time.Duration) {
	m.tickLatency.Observe(d.Seconds())
}

// RegisterRoomGauges wires the active-room-count and inbox/broadcast
// channel-buffer-usage gauges against registry, computed at scrape time
// (GaugeFunc), not polled on a timer.
func (m *Metrics) RegisterRoomGauges(registry *room.Registry) {
	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "aviron_rooms_active",
		Help: "Number of race rooms currently running.",
	}, func() float64 { return float64(registry.Count()) }))

	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name:        "aviron_channel_buffer_used",
		Help:        "Messages currently queued in a channel, summed across every live room/connection.",
		ConstLabels: prometheus.Labels{"channel": "inbox"},
	}, func() float64 { return float64(registry.InboxBufferUsage()) }))

	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name:        "aviron_channel_buffer_used",
		Help:        "Messages currently queued in a channel, summed across every live room/connection.",
		ConstLabels: prometheus.Labels{"channel": "broadcast"},
	}, func() float64 { return float64(registry.BroadcastBufferUsage()) }))
}

// RegisterPgPoolGauges wires aviron_pg_pool_acquired_conns/_max_conns
// (alerting/alert-rules.md's PostgresPoolSaturation rule) — race-service
// only, the one binary running pgxpool. Computed at scrape time
// (GaugeFunc) from pool.Stat(), which is itself safe for concurrent use
// alongside the pool's own query traffic.
func (m *Metrics) RegisterPgPoolGauges(pool *pgxpool.Pool) {
	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "aviron_pg_pool_acquired_conns",
		Help: "Postgres connection pool: connections currently acquired.",
	}, func() float64 { return float64(pool.Stat().AcquiredConns()) }))

	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "aviron_pg_pool_max_conns",
		Help: "Postgres connection pool: configured maximum connections.",
	}, func() float64 { return float64(pool.Stat().MaxConns()) }))
}

// RegisterNATSReconnectCounter wires aviron_nats_reconnects_total
// (alerting/alert-rules.md's NATSReconnectStorm rule) — a plain Counter
// the caller Incs from nats.ReconnectHandler, making a *pattern* of
// reconnects visible rather than nats.go's own internal, silent retry
// loop (this project's NATS Core setup, no JetStream).
func (m *Metrics) RegisterNATSReconnectCounter() prometheus.Counter {
	c := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "aviron_nats_reconnects_total",
		Help: "Number of times this process's NATS connection has reconnected after a disconnect.",
	})
	m.reg.MustRegister(c)
	return c
}

// Handler serves the Prometheus text-format exposition of this registry —
// GET /metrics, wired directly on the mux (no auth, no CORS: a scraper
// carries no JWT and is never called from a browser).
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}

// Registerer exposes this process's single prometheus.Registry so leaf
// infrastructure packages (internal/roomrelay, internal/roomlocator) can
// register their own metrics into it at construction time
// (metrics/metrics-parity.md), without either package needing to import
// internal/metrics itself — the same one-directional shape TickObserver
// already establishes for internal/room, just via prometheus.Registerer
// (a third-party interface) rather than a package-local one, since Bus and
// Locator are already leaf wrappers directly around nats.Conn/redis.Client
// with none of the transport-free layering concern room-actor-core.md
// raised for internal/room's own business logic.
func (m *Metrics) Registerer() prometheus.Registerer {
	return m.reg
}
