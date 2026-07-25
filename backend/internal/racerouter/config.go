package racerouter

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config is cmd/race-router's own small config — separate from
// internal/config.Config, which carries race-service fields (DatabaseURL,
// JWTSecret, ...) this process has no use for.
type Config struct {
	ListenAddr string
	RedisURL   string
	// Backends is the static, config-provided pool of race-service
	// addresses (host:port) round-robin'd across for room-less requests —
	// no dynamic service discovery at this project's scale.
	Backends []string
	CacheTTL time.Duration
}

// LoadConfig reads Config from the environment. RACE_SERVICE_INSTANCES is
// required — a router with no backends can't do its one job — everything
// else has a default.
func LoadConfig() (Config, error) {
	// Ignore the error: a missing .env is expected in production, where
	// config comes from real environment variables instead.
	_ = godotenv.Load()

	backends := splitAndTrim(os.Getenv("RACE_SERVICE_INSTANCES"))
	if len(backends) == 0 {
		return Config{}, fmt.Errorf("racerouter: RACE_SERVICE_INSTANCES is required (comma-separated host:port list)")
	}

	return Config{
		ListenAddr: getEnv("RACE_ROUTER_LISTEN_ADDR", ":8090"),
		RedisURL:   getEnv("REDIS_URL", "redis://localhost:6379/0"),
		Backends:   backends,
		CacheTTL:   getEnvDuration("ROUTING_CACHE_TTL", 30*time.Second),
	}, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
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
