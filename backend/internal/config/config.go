package config

import (
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	DatabaseURL string
	Port        string
}

func Load() Config {
	// Ignore the error: a missing .env is expected in production, where
	// config comes from real environment variables instead.
	_ = godotenv.Load()

	return Config{
		DatabaseURL: getEnv("DATABASE_URL", "postgres://aviron:aviron@localhost:5432/aviron?sslmode=disable"),
		Port:        getEnv("PORT", "8080"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
