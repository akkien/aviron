package config

import (
	"os"
	"strconv"

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
	}
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
