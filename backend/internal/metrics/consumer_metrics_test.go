package metrics_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/consumer"
	"github.com/akkien/aviron/internal/metrics"
)

func scrapeConsumer(t *testing.T, m *metrics.ConsumerMetrics) string {
	t.Helper()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	m.Handler().ServeHTTP(rec, req)

	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("read scrape body: %v", err)
	}
	return string(body)
}

func TestConsumerMetrics_RegisterLagGauge_ExposesZeroBeforeRun(t *testing.T) {
	m := metrics.NewConsumerMetrics()
	c := consumer.NewConsumer(nil, nil, nil, nil, consumer.NoopMetricsRecorder{}, slog.New(slog.DiscardHandler))
	m.RegisterLagGauge(c)

	body := scrapeConsumer(t, m)
	wantSubstrings := []string{
		`aviron_kafka_consumer_lag{topic="workout.sample"} 0`,
		`aviron_kafka_consumer_lag{topic="race.finished"} 0`,
		"go_goroutines ",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Errorf("scrape output missing %q\nfull output:\n%s", want, body)
		}
	}
}

func TestConsumerMetrics_ObserveBatchInsertAndIncDLQ(t *testing.T) {
	m := metrics.NewConsumerMetrics()

	m.ObserveBatchInsert("workout.sample", 10*time.Millisecond, nil)
	m.ObserveBatchInsert("workout.sample", 5*time.Millisecond, errors.New("boom"))
	m.IncDLQ("workout.sample.dlq")

	body := scrapeConsumer(t, m)
	wantSubstrings := []string{
		`aviron_consumer_batch_insert_duration_seconds_count{topic="workout.sample"} 2`,
		`aviron_consumer_batch_insert_errors_total{topic="workout.sample"} 1`,
		`aviron_consumer_dlq_total{topic="workout.sample.dlq"} 1`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Errorf("scrape output missing %q\nfull output:\n%s", want, body)
		}
	}
}
