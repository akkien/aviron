package httpserver_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/config"
	"github.com/akkien/aviron/internal/httpserver"
	"github.com/akkien/aviron/internal/room"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestMetrics_EndToEndThroughRealRouteRegistration proves the full
// composition wired in route.go — Metrics constructed before Registry
// (Registry needs it as a room.TickObserver), RegisterRoomGauges called
// once Registry exists, RegisterWSGauges called once WSHandler exists —
// actually works together, not just internal/metrics's own unit tests in
// isolation. Doesn't need a reachable Postgres: GET /metrics never touches
// pool, and pgxpool.New doesn't eagerly connect (TestHealthz_DBUnreachable
// already establishes this same pattern).
func TestMetrics_EndToEndThroughRealRouteRegistration(t *testing.T) {
	dsn := "postgres://aviron:aviron@localhost:1/aviron?sslmode=disable&connect_timeout=1"

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()

	m := newTestMetrics()
	registry := room.NewRegistry(testLogger, m)

	mux := httpserver.NewServer()
	httpserver.RegisterRoutes(mux, config.Config{}, pool, ctx, registry, testLogger, m)
	srv := newTestServer(t, mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	for _, want := range []string{
		"aviron_rooms_active 0",
		"aviron_connections_active 0",
		`aviron_channel_buffer_used{channel="inbox"} 0`,
		`aviron_channel_buffer_used{channel="broadcast"} 0`,
		`aviron_channel_buffer_used{channel="conn"} 0`,
		"aviron_tick_latency_seconds",
		"go_goroutines ",
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("GET /metrics response missing %q\nfull body:\n%s", want, body)
		}
	}
}
