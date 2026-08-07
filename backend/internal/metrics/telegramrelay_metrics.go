package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// TelegramRelayMetrics is telegram-relay's own metrics registry — same
// "separate registry per binary, nothing shared beyond the Go/process
// collectors" precedent GatewayMetrics/ConsumerMetrics already established.
type TelegramRelayMetrics struct {
	reg    *prometheus.Registry
	errors prometheus.Counter
}

// NewTelegramRelayMetrics constructs the registry, the standard Go
// process/runtime collectors, and aviron_telegram_relay_errors_total
// (telegram-relay.md).
func NewTelegramRelayMetrics() *TelegramRelayMetrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	errors := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "aviron_telegram_relay_errors_total",
		Help: "Webhook calls where forwarding the alert to the Telegram Bot API failed.",
	})
	reg.MustRegister(errors)

	return &TelegramRelayMetrics{reg: reg, errors: errors}
}

// IncError satisfies telegramrelay.ErrorRecorder.
func (m *TelegramRelayMetrics) IncError() {
	m.errors.Inc()
}

// Handler serves this registry's Prometheus text-format exposition —
// GET /metrics.
func (m *TelegramRelayMetrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
