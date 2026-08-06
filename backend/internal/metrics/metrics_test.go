package metrics_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/akkien/aviron/internal/metrics"
	"github.com/akkien/aviron/internal/room"
)

var testLogger = slog.New(slog.DiscardHandler)

func scrape(t *testing.T, m *metrics.Metrics) string {
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

func TestMetrics_ExposesRoomAndRuntimeMetrics(t *testing.T) {
	m := metrics.NewMetrics()
	registry := room.NewRegistry(testLogger, m, room.NoopLocator{}, room.NoopPublisher{}, room.NoopRoomBus{}, room.NoopEvictionRecorder{})

	m.RegisterRoomGauges(registry)

	body := scrape(t, m)

	// Active room count, channel buffer usage (2 labels: inbox/broadcast —
	// "conn" was race-service-side WS connection buffering, removed along
	// with RegisterWSGauges when room-service-adapter.md relocated
	// connection-holding code out of this process), and broadcast tick
	// latency — project-overview.md §9's metrics minus connection
	// count/goroutine count (goroutine count is satisfied by the
	// auto-registered go_goroutines below, not a duplicate custom gauge;
	// connection count moves to whatever process holds WS connections next,
	// per ws-gateway.md).
	wantSubstrings := []string{
		"aviron_rooms_active 0",
		`aviron_channel_buffer_used{channel="inbox"} 0`,
		`aviron_channel_buffer_used{channel="broadcast"} 0`,
		"aviron_tick_latency_seconds",
		"go_goroutines ",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Errorf("scrape output missing %q\nfull output:\n%s", want, body)
		}
	}
}

func TestMetrics_ObserveTick_AppearsInScrape(t *testing.T) {
	m := metrics.NewMetrics()

	m.ObserveTick(42 * time.Millisecond)

	body := scrape(t, m)
	if !strings.Contains(body, "aviron_tick_latency_seconds_count 1") {
		t.Errorf("scrape output missing a single tick_latency observation\nfull output:\n%s", body)
	}
}

func TestMetrics_RegisterRoomGauges_ReflectsRoomCount(t *testing.T) {
	m := metrics.NewMetrics()
	registry := room.NewRegistry(testLogger, m, room.NoopLocator{}, room.NoopPublisher{}, room.NoopRoomBus{}, room.NoopEvictionRecorder{})
	m.RegisterRoomGauges(registry)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry.Spawn(ctx, "race-1", 5, noopFinisher{}, noopLeaver{}, noopCanceller{})

	body := scrape(t, m)
	if !strings.Contains(body, "aviron_rooms_active 1") {
		t.Errorf("scrape output missing aviron_rooms_active 1 after Spawn\nfull output:\n%s", body)
	}
}

func TestMetrics_Registerer_AllowsExternalRegistration(t *testing.T) {
	m := metrics.NewMetrics()

	c := prometheus.NewCounter(prometheus.CounterOpts{Name: "test_counter"})
	if err := m.Registerer().Register(c); err != nil {
		t.Fatalf("Register: %v", err)
	}
	c.Inc()

	body := scrape(t, m)
	if !strings.Contains(body, "test_counter 1") {
		t.Errorf("scrape output missing test_counter 1\nfull output:\n%s", body)
	}
}

// TestMetrics_RegisterPgPoolGauges_ExposesAcquiredAndMaxConns doesn't need
// a reachable Postgres: pgxpool.New doesn't eagerly connect (same pattern
// internal/httpserver's own metrics/healthz tests already rely on), and
// Stat() reports pool-internal counters that are valid before any
// connection is ever made.
func TestMetrics_RegisterPgPoolGauges_ExposesAcquiredAndMaxConns(t *testing.T) {
	dsn := "postgres://aviron:aviron@localhost:1/aviron?sslmode=disable&connect_timeout=1"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()

	m := metrics.NewMetrics()
	m.RegisterPgPoolGauges(pool)

	body := scrape(t, m)
	wantSubstrings := []string{
		"aviron_pg_pool_acquired_conns 0",
		"aviron_pg_pool_max_conns ",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(body, want) {
			t.Errorf("scrape output missing %q\nfull output:\n%s", want, body)
		}
	}
}

func TestMetrics_RegisterNATSReconnectCounter_IncrementsAndScrapes(t *testing.T) {
	m := metrics.NewMetrics()
	c := m.RegisterNATSReconnectCounter()

	c.Inc()
	c.Inc()

	body := scrape(t, m)
	if !strings.Contains(body, "aviron_nats_reconnects_total 2") {
		t.Errorf("scrape output missing aviron_nats_reconnects_total 2\nfull output:\n%s", body)
	}
}

type noopFinisher struct{}

func (noopFinisher) FinishRace(ctx context.Context, raceID string, distanceMeters int, results []room.ParticipantResult) error {
	return nil
}

type noopLeaver struct{}

func (noopLeaver) LeaveRace(ctx context.Context, raceID, userID string) error { return nil }

type noopCanceller struct{}

func (noopCanceller) CancelRace(ctx context.Context, raceID string) error { return nil }
