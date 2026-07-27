package wsgateway

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
)

// NewHealthzHandler serves GET /healthz — unauthenticated, no Cors wrapper,
// mirroring internal/httpserver.NewHealthzHandler's shape so this project
// has one consistent health-check convention across every binary
// (ws-gateway.md, closing a real gap the deleted race-router had: it never
// exposed a health endpoint at all). Checks, on every call, both
// dependencies this process's actual job depends on — Redis (Owner()
// lookups, the evicted-reconnect check) and NATS (internal/roomrelay) — a
// gateway that can reach neither is not meaningfully "up," even if the
// process itself is still serving. No readiness/liveness split: this
// process holds no background state whose liveness could plausibly diverge
// from its readiness the way a room actor's tick loop could.
func NewHealthzHandler(redisClient *redis.Client, natsConn *nats.Conn) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		w.Header().Set("Content-Type", "application/json")

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
