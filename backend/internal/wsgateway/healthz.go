package wsgateway

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
)

// ReadinessGate lets cmd/ws-gateway's SIGTERM handler flip GET /healthz to
// unready the instant the signal arrives (graceful-shutdown.md) — before
// http.Server.Shutdown even begins draining in-flight connections, so
// Kubernetes' readiness-based traffic removal starts as early as
// possible. Deliberately not shared with GET /livez: liveness must stay
// "ok" for as long as the process is actually still running and able to
// answer requests, shutting down gracefully or not. A small, independent
// duplicate of internal/httpserver.ReadinessGate — this package already
// has its own healthz convention rather than a shared one, and a shared
// package for a two-method atomic-bool wrapper isn't worth introducing.
type ReadinessGate struct {
	shuttingDown atomic.Bool
}

// MarkShuttingDown flips the gate. Safe to call more than once.
func (g *ReadinessGate) MarkShuttingDown() {
	g.shuttingDown.Store(true)
}

// ShuttingDown reports whether MarkShuttingDown has been called.
func (g *ReadinessGate) ShuttingDown() bool {
	return g.shuttingDown.Load()
}

// NewHealthzHandler serves GET /healthz — unauthenticated, no Cors wrapper,
// mirroring internal/httpserver.NewHealthzHandler's shape so this project
// has one consistent health-check convention across every binary
// (ws-gateway.md, closing a real gap the deleted race-router had: it never
// exposed a health endpoint at all). Checks, on every call, both
// dependencies this process's actual job depends on — Redis (Owner()
// lookups, the evicted-reconnect check) and NATS (internal/roomrelay) — a
// gateway that can reach neither is not meaningfully "up," even if the
// process itself is still serving.
//
// Correction (graceful-shutdown.md): this handler's own doc comment used
// to argue no readiness/liveness split was needed here ("this process
// holds no background state whose liveness could plausibly diverge from
// its readiness"). That's still true for the dependency checks below, but
// it missed a real case: once SIGTERM arrives, this pod should stop
// receiving new traffic immediately, without waiting on a Redis/NATS
// round-trip to say so — and reusing this same check for a
// livenessProbe would make kubelet restart an otherwise-healthy pod over
// a transient Redis/NATS blip. See NewLivezHandler for the dependency-free
// counterpart.
// syncedChecker is optionally satisfied by a BackendDiscovery
// implementation with an async initial-sync phase (k8sBackendDiscovery).
// StaticBackends doesn't implement it — it's synced the instant it's
// constructed, nothing to wait on — so the type assertion in
// NewHealthzHandler below simply skips this check for local
// go run/docker-compose, unchanged from before this discovery existed.
type syncedChecker interface {
	HasSynced() bool
}

func NewHealthzHandler(redisClient *redis.Client, natsConn *nats.Conn, discovery BackendDiscovery, gate *ReadinessGate) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if gate.ShuttingDown() {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"status": "shutting_down"})
			return
		}

		// dynamic-backend-discovery.md: a freshly-started pod must not
		// report ready before its informer's first List completes, or it
		// would start receiving room-less traffic with an empty backend
		// pool. Non-blocking — HasSynced is a plain bool read, unlike
		// WaitForSync, which this handler must never call.
		if sc, ok := discovery.(syncedChecker); ok && !sc.HasSynced() {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"status": "backend_discovery_not_synced"})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if err := redisClient.Ping(ctx).Err(); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"status": "redis_unreachable"})
			return
		}

		// A local connection-state check, not a round trip — nats.go's
		// client already tracks this itself.
		if natsConn.Status() != nats.CONNECTED {
			w.WriteHeader(http.StatusServiceUnavailable)
			json.NewEncoder(w).Encode(map[string]string{"status": "nats_unreachable"})
			return
		}

		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

// NewLivezHandler serves GET /livez — 200 as long as this process is
// running and able to answer requests at all, no dependency checks. See
// NewHealthzHandler's "Correction" note for why this exists separately.
func NewLivezHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}
