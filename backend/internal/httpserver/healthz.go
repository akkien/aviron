package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewHealthzHandler godoc
// @Summary Readiness check
// @Description Pings the database connection pool and reports service readiness
// @Tags system
// @Produce json
// @Success 200 {object} map[string]string "status: ok"
// @Failure 503 {object} map[string]string "status: db_unreachable"
// @Failure 503 {object} map[string]string "status: shutting_down"
// @Router /healthz [get]
func NewHealthzHandler(pool *pgxpool.Pool, gate *ReadinessGate) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Checked first, before the Postgres round-trip below: once
		// SIGTERM has arrived there's no need to wait on a real Ping to
		// know this pod should stop receiving new traffic
		// (graceful-shutdown.md).
		if gate.ShuttingDown() {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"status": "shutting_down"})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if err := pool.Ping(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"status": "db_unreachable"})
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// NewLivezHandler godoc
// @Summary Liveness check
// @Description Reports 200 as long as this process is running and able to answer requests at all — no dependency checks, deliberately: a livenessProbe wired to a dependency check would make kubelet restart an otherwise-healthy pod over a transient Postgres blip, not just remove it from Service rotation the way a failed readinessProbe correctly does.
// @Tags system
// @Produce json
// @Success 200 {object} map[string]string "status: ok"
// @Router /livez [get]
func NewLivezHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}
