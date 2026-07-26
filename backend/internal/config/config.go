package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/akkien/aviron/internal/race"
	"github.com/joho/godotenv"
)

type Config struct {
	DatabaseURL       string
	Port              string
	JWTSecret         string
	CORSAllowedOrigin string
	// PprofEnabled gates net/http/pprof's /debug/pprof/ endpoints
	// (project-overview.md §9) — no Env enum exists in this config yet, so
	// a single bool is proportionate to "enabled in dev/staging" rather
	// than an abstraction nothing else needs.
	PprofEnabled bool
	// InstanceID identifies this process in Redis room-ownership records
	// (redis-room-registry.md) — every instance needs a stable identity to
	// claim/release room ownership under.
	InstanceID string
	// RedisURL points at the shared Redis used for cross-instance room
	// ownership (redis-room-registry.md).
	RedisURL string
	// KafkaBrokers is a comma-separated list of broker addresses the event
	// producer publishes workout.sample/race.finished to (kafka-producer.md).
	KafkaBrokers string
}

func Load() *Config {
	// Ignore the error: a missing .env is expected in production, where
	// config comes from real environment variables instead.
	_ = godotenv.Load()

	return &Config{
		DatabaseURL:       getEnv("DATABASE_URL", "postgres://aviron:aviron@localhost:5432/aviron?sslmode=disable"),
		Port:              getEnv("PORT", "8080"),
		JWTSecret:         getEnv("JWT_SECRET", "dev-only-secret-change-me"),
		CORSAllowedOrigin: getEnv("CORS_ALLOWED_ORIGIN", "http://localhost:5173"),
		PprofEnabled:      getEnvBool("PPROF_ENABLED", true),
		InstanceID:        getEnvInstanceID(),
		RedisURL:          getEnv("REDIS_URL", "redis://localhost:6379/0"),
		KafkaBrokers:      getEnv("KAFKA_BROKERS", "localhost:9092"),
	}
}

// getEnvInstanceID returns INSTANCE_ID if set, otherwise generates one —
// most deployments won't set it explicitly, but every instance still needs
// a stable identity for the lifetime of the process to claim room ownership
// under (redis-room-registry.md).
func getEnvInstanceID() string {
	if v := os.Getenv("INSTANCE_ID"); v != "" {
		return v
	}
	id, err := race.GenerateRaceID()
	if err != nil {
		// crypto/rand failing is not something this process can recover
		// from — every other id in this codebase (race ids, JWTs) already
		// depends on it working.
		panic(fmt.Sprintf("config: generate instance id: %v", err))
	}
	return id
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
