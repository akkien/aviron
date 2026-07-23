package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/akkien/aviron/internal/room"
	"github.com/akkien/aviron/internal/ws"
)

// Metrics owns a private prometheus.Registry (not the global
// prometheus.DefaultRegisterer) so this package stays explicit and
// constructor-threaded rather than relying on mutable package-level state,
// consistent with this project's existing no-hidden-globals convention.
//
// Construction is split across NewMetrics, RegisterRoomGauges, and
// RegisterWSGauges rather than one shot because of a real ordering
// constraint: room.Registry needs a room.TickObserver (this type) at its
// own construction time, but the room/connection gauges below need a
// *room.Registry / *ws.WSHandler to read from — those don't exist until
// after Registry (and, in turn, WSHandler) are constructed. NewMetrics has
// zero dependencies so it can run first; the two Register* calls run once
// their argument actually exists.
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

// RegisterWSGauges wires the connection-count and per-connection
// channel-buffer-usage gauges against wsHandler, computed at scrape time.
func (m *Metrics) RegisterWSGauges(wsHandler *ws.WSHandler) {
	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "aviron_connections_active",
		Help: "Number of WebSocket connections currently being served.",
	}, func() float64 { return float64(wsHandler.ConnectionCount()) }))

	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name:        "aviron_channel_buffer_used",
		Help:        "Messages currently queued in a channel, summed across every live room/connection.",
		ConstLabels: prometheus.Labels{"channel": "conn"},
	}, func() float64 { return float64(wsHandler.ConnBufferUsage()) }))
}

// Handler serves the Prometheus text-format exposition of this registry —
// GET /metrics, wired directly on the mux (no auth, no CORS: a scraper
// carries no JWT and is never called from a browser).
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
