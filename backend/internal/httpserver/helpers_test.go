package httpserver_test

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/metrics"
)

// testLogger discards output — these tests assert on HTTP responses, not
// log lines.
var testLogger = slog.New(slog.DiscardHandler)

// testTickObserver discards tick-latency observations — these tests never
// spawn a room actor, but room.NewRegistry still requires a non-nil
// room.TickObserver.
type testTickObserverType struct{}

func (testTickObserverType) ObserveTick(d time.Duration) {}

var testTickObserver = testTickObserverType{}

// newTestMetrics constructs a fresh *metrics.Metrics — each call gets its
// own private prometheus.Registry, so tests that each call RegisterRoutes
// once don't collide on duplicate metric registration.
func newTestMetrics() *metrics.Metrics {
	return metrics.NewMetrics()
}

func newTestServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	return httptest.NewServer(h)
}

func getJSON(t *testing.T, url string) (*http.Response, map[string]string) {
	t.Helper()

	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()

	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}

	return resp, body
}
