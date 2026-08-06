package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/akkien/aviron/internal/consumer"
	"github.com/akkien/aviron/internal/kafka"
)

// ConsumerMetrics is the consumer binary's own metrics registry — see
// GatewayMetrics' identical doc comment for why this isn't shared with
// Metrics/GatewayMetrics.
type ConsumerMetrics struct {
	reg                 *prometheus.Registry
	batchInsertDuration *prometheus.HistogramVec
	batchInsertErrors   *prometheus.CounterVec
	dlqTotal            *prometheus.CounterVec
}

// NewConsumerMetrics constructs the registry, the standard Go
// process/runtime collectors, and the push-model metrics ObserveBatchInsert/
// IncDLQ record into — Consumer observes through this type via the
// consumer.MetricsRecorder interface it structurally satisfies, so
// internal/consumer itself never imports prometheus (mirrors
// internal/room's TickObserver — see Metrics.Registerer's doc comment for
// the same reasoning, applied here to internal/consumer's own business
// logic rather than a leaf infrastructure wrapper).
func NewConsumerMetrics() *ConsumerMetrics {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))

	batchInsertDuration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "aviron_consumer_batch_insert_duration_seconds",
		Help: "Duration of a single batch write to Postgres (InsertBatch or ReconcileParticipantResults).",
	}, []string{"topic"})
	batchInsertErrors := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "aviron_consumer_batch_insert_errors_total",
		Help: "Batch writes to Postgres that returned an error, transient or permanent.",
	}, []string{"topic"})
	dlqTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "aviron_consumer_dlq_total",
		Help: "Messages republished to a dead-letter topic.",
	}, []string{"topic"})
	reg.MustRegister(batchInsertDuration, batchInsertErrors, dlqTotal)

	return &ConsumerMetrics{
		reg:                 reg,
		batchInsertDuration: batchInsertDuration,
		batchInsertErrors:   batchInsertErrors,
		dlqTotal:            dlqTotal,
	}
}

// ObserveBatchInsert satisfies consumer.MetricsRecorder.
func (m *ConsumerMetrics) ObserveBatchInsert(topic string, d time.Duration, err error) {
	m.batchInsertDuration.WithLabelValues(topic).Observe(d.Seconds())
	if err != nil {
		m.batchInsertErrors.WithLabelValues(topic).Inc()
	}
}

// IncDLQ satisfies consumer.MetricsRecorder.
func (m *ConsumerMetrics) IncDLQ(topic string) {
	m.dlqTotal.WithLabelValues(topic).Inc()
}

// RegisterLagGauge wires aviron_kafka_consumer_lag{topic}, one GaugeFunc per
// topic reading c.Lag() at scrape time — kafka-go's *Reader.Stats() is
// itself safe for concurrent use alongside the reader's own fetch loop, so
// no polling goroutine is needed.
func (m *ConsumerMetrics) RegisterLagGauge(c *consumer.Consumer) {
	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name:        "aviron_kafka_consumer_lag",
		Help:        "Kafka consumer group lag, in messages, for this topic's reader.",
		ConstLabels: prometheus.Labels{"topic": kafka.TopicWorkoutSample},
	}, func() float64 {
		lag, _ := c.Lag()
		return float64(lag)
	}))
	m.reg.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name:        "aviron_kafka_consumer_lag",
		Help:        "Kafka consumer group lag, in messages, for this topic's reader.",
		ConstLabels: prometheus.Labels{"topic": kafka.TopicRaceFinished},
	}, func() float64 {
		_, lag := c.Lag()
		return float64(lag)
	}))
}

// Handler serves this registry's Prometheus text-format exposition —
// GET /metrics.
func (m *ConsumerMetrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{})
}
