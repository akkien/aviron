package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/akkien/aviron/internal/wsgateway"
)

// GatewayMetrics is ws-gateway's own metrics registry — a separate type
// from Metrics (race-service's), not a shared one, since the two processes
// have nothing in common to register beyond the Go/process collectors
// (metrics/metrics-parity.md's "Where the code lives").
type GatewayMetrics struct {
	reg *prometheus.Registry
}

// NewGatewayMetrics constructs the registry plus the standard Go
// process/runtime collectors, same as NewMetrics.
func NewGatewayMetrics() *GatewayMetrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return &GatewayMetrics{reg: reg}
}

// RegisterConnectionGauge wires aviron_ws_connections_active — the rebuild
// of the connection-count gauge Metrics' own doc comment flagged as owed to
// whatever process ended up holding WebSocket connections (ws-gateway, per
// room-service-adapter.md). Computed at scrape time (GaugeFunc), summing
// every raceHub's connection count via RaceHubRegistry.Count.
func (m *GatewayMetrics) RegisterConnectionGauge(hubs *wsgateway.RaceHubRegistry) {
	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "aviron_ws_connections_active",
		Help: "Number of local WebSocket connections this gateway instance currently holds, summed across every race.",
	}, func() float64 { return float64(hubs.Count()) }))
}

// Registerer exposes this process's registry so internal/roomrelay and
// internal/roomlocator can register their own publish/lookup metrics into
// it — see Metrics.Registerer's identical doc comment for the reasoning.
func (m *GatewayMetrics) Registerer() prometheus.Registerer {
	return m.reg
}

// Handler serves this registry's Prometheus text-format exposition —
// GET /metrics, unauthenticated/uncors'd like race-service's.
func (m *GatewayMetrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
