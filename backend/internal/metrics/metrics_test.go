package metrics_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/metrics"
	"github.com/akkien/aviron/internal/room"
	"github.com/akkien/aviron/internal/ws"
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

func TestMetrics_ExposesAllFiveRequiredMetrics(t *testing.T) {
	m := metrics.NewMetrics()
	registry := room.NewRegistry(testLogger, m, room.NoopLocator{})
	wsHandler := ws.NewWSHandler(registry, []byte("test-secret"), "http://localhost:5173", testLogger)

	m.RegisterRoomGauges(registry)
	m.RegisterWSGauges(wsHandler)

	body := scrape(t, m)

	// Active room count, connection count, channel buffer usage (3 labels),
	// and broadcast tick latency — project-overview.md §9's 5 metrics,
	// minus goroutine count (satisfied by the auto-registered go_goroutines
	// below, not a duplicate custom gauge).
	wantSubstrings := []string{
		"aviron_rooms_active 0",
		"aviron_connections_active 0",
		`aviron_channel_buffer_used{channel="inbox"} 0`,
		`aviron_channel_buffer_used{channel="broadcast"} 0`,
		`aviron_channel_buffer_used{channel="conn"} 0`,
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
	registry := room.NewRegistry(testLogger, m, room.NoopLocator{})
	m.RegisterRoomGauges(registry)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	registry.Spawn(ctx, "race-1", 5, noopFinisher{}, noopLeaver{}, noopCanceller{})

	body := scrape(t, m)
	if !strings.Contains(body, "aviron_rooms_active 1") {
		t.Errorf("scrape output missing aviron_rooms_active 1 after Spawn\nfull output:\n%s", body)
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
