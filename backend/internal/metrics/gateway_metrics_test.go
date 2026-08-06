package metrics_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/akkien/aviron/internal/metrics"
	"github.com/akkien/aviron/internal/roomrelay"
	"github.com/akkien/aviron/internal/wsgateway"
)

func scrapeGateway(t *testing.T, m *metrics.GatewayMetrics) string {
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

func TestGatewayMetrics_RegisterConnectionGauge_ReflectsHubCount(t *testing.T) {
	m := metrics.NewGatewayMetrics()
	hubs := wsgateway.NewRaceHubRegistry(context.Background(), roomrelay.NewFakeBus(), slog.New(slog.DiscardHandler))
	m.RegisterConnectionGauge(hubs)

	body := scrapeGateway(t, m)
	wantSubstrings := []string{
		"aviron_ws_connections_active 0",
		"go_goroutines ",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Errorf("scrape output missing %q\nfull output:\n%s", want, body)
		}
	}
}

func TestGatewayMetrics_Registerer_AllowsExternalRegistration(t *testing.T) {
	m := metrics.NewGatewayMetrics()

	c := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_counter"})
	if err := m.Registerer().Register(c); err != nil {
		t.Fatalf("Register: %v", err)
	}
	c.Inc()

	body := scrapeGateway(t, m)
	if !strings.Contains(body, "test_counter 1") {
		t.Errorf("scrape output missing test_counter 1\nfull output:\n%s", body)
	}
}
