package wsgateway

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config is cmd/ws-gateway's own small config — separate from
// internal/config.Config, which carries race-service fields (DatabaseURL,
// ...) this process has no use for. Ported from the deleted
// internal/racerouter.Config (ws-gateway.md), plus JWTSecret and NATSURL:
// JWTSecret is new relative to that config — race-router never inspected a
// token at all, but ws-gateway has to know who a connection belongs to in
// order to construct InboundEnvelopes, so verifying identity is now
// unavoidably this process's own job.
type Config struct {
	ListenAddr string
	RedisURL   string
	NATSURL    string
	JWTSecret  []byte
	// AllowedOrigin is missing from ws-gateway.md's own Config sketch, but
	// WSHandler.ServeHTTP's websocket.Accept call needs one regardless of
	// spec — the same reason race-service's own WSHandler needed
	// cfg.CORSAllowedOrigin: without it, coder/websocket's default
	// same-origin check would reject the frontend's cross-origin handshake
	// in local dev. Proxied REST requests don't need this — the race-
	// service backend they're forwarded to already applies its own CORS
	// headers via middleware.Cors, which pass through the proxy unchanged;
	// only this process's own direct Accept call needs to know the origin.
	AllowedOrigin string
	// Backends is the static, config-provided pool of race-service
	// addresses (host:port) round-robin'd across for room-less requests —
	// no dynamic service discovery at this project's scale, same stance
	// race-router.md already carried.
	Backends []string
	CacheTTL time.Duration
	// PprofEnabled gates net/http/pprof's /debug/pprof/ endpoints — same
	// PPROF_ENABLED key internal/config.Config already reads for
	// race-service, kept as a separate field here since this is a distinct
	// Config type (metrics/metrics-parity.md).
	PprofEnabled bool
}

// LoadConfig reads Config from the environment. RACE_SERVICE_INSTANCES is
// required — a gateway with no backends can't proxy REST requests anywhere.
func LoadConfig() (Config, error) {
	// Ignore the error: a missing .env is expected in production, where
	// config comes from real environment variables instead.
	_ = godotenv.Load()

	backends := splitAndTrim(os.Getenv("RACE_SERVICE_INSTANCES"))
	if len(backends) == 0 {
		return Config{}, fmt.Errorf("wsgateway: RACE_SERVICE_INSTANCES is required (comma-separated host:port list)")
	}

	return Config{
		ListenAddr: getEnv("WS_GATEWAY_LISTEN_ADDR", ":8090"),
		RedisURL:   getEnv("REDIS_URL", "redis://localhost:6379/0"),
		NATSURL:    getEnv("NATS_URL", "nats://localhost:4222"),
		JWTSecret:  []byte(getEnv("JWT_SECRET", "dev-only-secret-change-me")),
		// Same env var name and default internal/config.Config already
		// uses for race-service's own CORS middleware — one shared
		// frontend origin, one shared setting.
		AllowedOrigin: getEnv("CORS_ALLOWED_ORIGIN", "http://localhost:5173"),
		Backends:      backends,
		CacheTTL:      getEnvDuration("ROUTING_CACHE_TTL", 30*time.Second),
		PprofEnabled:  getEnvBool("PPROF_ENABLED", true),
	}, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func splitAndTrim(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
