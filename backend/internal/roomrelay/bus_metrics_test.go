package roomrelay

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	natsserver "github.com/nats-io/nats-server/v2/test"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// newTestBusWithRegistry mirrors newTestBus but also returns the registry
// its metrics were registered into, so a test can scrape it directly.
func newTestBusWithRegistry(t *testing.T) (*Bus, *prometheus.Registry) {
	t.Helper()
	srv := natsserver.RunRandClientPortServer()
	t.Cleanup(srv.Shutdown)

	nc, err := nats.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("nats.Connect: %v", err)
	}
	t.Cleanup(nc.Close)

	reg := prometheus.NewRegistry()
	return NewBus(nc, reg), reg
}

func scrapeRegistry(t *testing.T, reg *prometheus.Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	promhttp.HandlerFor(reg, promhttp.HandlerOpts{}).ServeHTTP(rec, req)

	body, err := io.ReadAll(rec.Result().Body)
	if err != nil {
		t.Fatalf("read scrape body: %v", err)
	}
	return string(body)
}

func TestBus_PublishIn_RecordsPublishMetrics(t *testing.T) {
	b, reg := newTestBusWithRegistry(t)

	if err := b.PublishIn(context.Background(), "race-1", InboundEnvelope{Kind: InboundKindMessage, RaceID: "race-1", UserID: "u1"}); err != nil {
		t.Fatalf("PublishIn: %v", err)
	}

	body := scrapeRegistry(t, reg)
	wantSubstrings := []string{
		`aviron_roomrelay_publish_total{subject_kind="in"} 1`,
		`aviron_roomrelay_publish_duration_seconds_count{subject_kind="in"} 1`,
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Errorf("scrape output missing %q\nfull output:\n%s", want, body)
		}
	}
	if strings.Contains(body, `aviron_roomrelay_publish_errors_total{subject_kind="in"}`) {
		t.Errorf("expected no publish_errors_total series for a successful publish\nfull output:\n%s", body)
	}
}

func TestBus_PublishOut_RecordsPublishMetrics(t *testing.T) {
	b, reg := newTestBusWithRegistry(t)

	if err := b.PublishOut(context.Background(), "race-1", OutboundEnvelope{Kind: OutboundKindBroadcast, RaceID: "race-1"}); err != nil {
		t.Fatalf("PublishOut: %v", err)
	}

	body := scrapeRegistry(t, reg)
	if !strings.Contains(body, `aviron_roomrelay_publish_total{subject_kind="out"} 1`) {
		t.Errorf("scrape output missing aviron_roomrelay_publish_total{subject_kind=\"out\"} 1\nfull output:\n%s", body)
	}
}

func TestBus_PublishIn_RecordsErrorMetricOnCancelledContext(t *testing.T) {
	b, reg := newTestBusWithRegistry(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := b.PublishIn(ctx, "race-1", InboundEnvelope{Kind: InboundKindMessage, RaceID: "race-1", UserID: "u1"}); err == nil {
		t.Fatal("expected PublishIn against a cancelled context to return an error")
	}

	body := scrapeRegistry(t, reg)
	if !strings.Contains(body, `aviron_roomrelay_publish_errors_total{subject_kind="in"} 1`) {
		t.Errorf("scrape output missing aviron_roomrelay_publish_errors_total{subject_kind=\"in\"} 1\nfull output:\n%s", body)
	}
}
