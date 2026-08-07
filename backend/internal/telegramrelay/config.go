package telegramrelay

import (
	"os"

	"github.com/joho/godotenv"
)

// Config is telegram-relay's own small config — separate from
// internal/config.Config, same "distinct binary, distinct config type"
// precedent internal/wsgateway.Config's own doc comment already
// established.
type Config struct {
	BotToken   string
	ChatID     string
	ListenAddr string
}

// LoadConfig reads Config from the environment. BotToken/ChatID are left
// empty rather than required if unset — same soft-fallback treatment this
// project already gives JWTSecret, not the hard failure RACE_SERVICE_
// INSTANCES gets: a missing token doesn't stop this process from starting
// or serving /alert, it only makes Relay.Notify's own Telegram API calls
// fail (logged, counted via aviron_telegram_relay_errors_total).
func LoadConfig() Config {
	// Ignore the error: a missing .env is expected in production, where
	// config comes from real environment variables instead.
	_ = godotenv.Load()

	return Config{
		BotToken:   os.Getenv("TELEGRAM_BOT_TOKEN"),
		ChatID:     os.Getenv("TELEGRAM_CHAT_ID"),
		ListenAddr: getEnv("TELEGRAM_RELAY_LISTEN_ADDR", ":8080"),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
