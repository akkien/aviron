package metrics_test

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/akkien/aviron/internal/metrics"
)

func scrapeTelegramRelay(t *testing.T, m *metrics.TelegramRelayMetrics) string {
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

func TestTelegramRelayMetrics_IncError(t *testing.T) {
	m := metrics.NewTelegramRelayMetrics()

	m.IncError()
	m.IncError()

	body := scrapeTelegramRelay(t, m)
	wantSubstrings := []string{
		"aviron_telegram_relay_errors_total 2",
		"go_goroutines ",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Errorf("scrape output missing %q\nfull output:\n%s", want, body)
		}
	}
}
