package httpserver_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/akkien/aviron/internal/config"
	"github.com/akkien/aviron/internal/httpserver"
	"github.com/akkien/aviron/internal/room"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestHealthz_OK(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://aviron:aviron@localhost:5432/aviron?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		t.Skipf("skipping: no reachable Postgres at %s (%v) — run `docker compose up -d postgres`", dsn, err)
	}

	mux := httpserver.NewServer()
	httpserver.RegisterRoutes(mux, config.Config{}, pool, ctx, room.NewRegistry(testLogger, testTickObserver, room.NoopLocator{}), testLogger, newTestMetrics())
	srv := newTestServer(t, mux)
	defer srv.Close()

	resp, body := getJSON(t, srv.URL+"/healthz")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if body["status"] != "ok" {
		t.Fatalf("body[status] = %q, want %q", body["status"], "ok")
	}
}

func TestHealthz_DBUnreachable(t *testing.T) {
	// Port 1 has nothing listening, so pgxpool.Ping fails fast without a real DB.
	dsn := "postgres://aviron:aviron@localhost:1/aviron?sslmode=disable&connect_timeout=1"

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("open pool: %v", err)
	}
	defer pool.Close()

	mux := httpserver.NewServer()
	httpserver.RegisterRoutes(mux, config.Config{}, pool, ctx, room.NewRegistry(testLogger, testTickObserver, room.NoopLocator{}), testLogger, newTestMetrics())
	srv := newTestServer(t, mux)
	defer srv.Close()

	resp, body := getJSON(t, srv.URL+"/healthz")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
	}
	if body["status"] != "db_unreachable" {
		t.Fatalf("body[status] = %q, want %q", body["status"], "db_unreachable")
	}
}
