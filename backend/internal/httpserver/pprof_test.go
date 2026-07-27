package httpserver_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/config"
	"github.com/akkien/aviron/internal/httpserver"
	"github.com/akkien/aviron/internal/room"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newTestPool returns an unreachable-but-valid pool — pgxpool.New doesn't
// eagerly connect, and neither GET /debug/pprof/ nor its gated absence ever
// touches the pool, the same pattern TestHealthz_DBUnreachable and
// TestMetrics_EndToEndThroughRealRouteRegistration already establish.
func newTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), "postgres://aviron:aviron@localhost:1/aviron?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestPprof_EnabledServesDebugEndpoints(t *testing.T) {
	pool := newTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	mux := httpserver.NewServer()
	httpserver.RegisterRoutes(mux, config.Config{PprofEnabled: true}, pool, ctx, room.NewRegistry(testLogger, testTickObserver, room.NoopLocator{}, room.NoopPublisher{}, room.NoopRoomBus{}, room.NoopEvictionRecorder{}), testLogger, newTestMetrics())
	srv := newTestServer(t, mux)
	defer srv.Close()

	for _, path := range []string{"/debug/pprof/", "/debug/pprof/goroutine", "/debug/pprof/cmdline"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s: status = %d, want %d", path, resp.StatusCode, http.StatusOK)
		}
	}
}

func TestPprof_DisabledReturns404(t *testing.T) {
	pool := newTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	mux := httpserver.NewServer()
	httpserver.RegisterRoutes(mux, config.Config{PprofEnabled: false}, pool, ctx, room.NewRegistry(testLogger, testTickObserver, room.NoopLocator{}, room.NoopPublisher{}, room.NoopRoomBus{}, room.NoopEvictionRecorder{}), testLogger, newTestMetrics())
	srv := newTestServer(t, mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/debug/pprof/")
	if err != nil {
		t.Fatalf("GET /debug/pprof/: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d (pprof should not be registered when disabled)", resp.StatusCode, http.StatusNotFound)
	}
}
