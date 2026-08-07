package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/akkien/aviron/internal/metrics"
	"github.com/akkien/aviron/internal/telegramrelay"
)

// shutdownTimeout bounds how long Shutdown waits for the one in-flight
// /alert call this process might be mid-handling — this binary has no
// in-progress work of its own beyond that single HTTP call, unlike
// race-service's rooms or ws-gateway's connections.
const shutdownTimeout = 10 * time.Second

func Run(cfg telegramrelay.Config) {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	if cfg.BotToken == "" || cfg.ChatID == "" {
		logger.Warn("telegram-relay: TELEGRAM_BOT_TOKEN/TELEGRAM_CHAT_ID not set, sendMessage calls will fail")
	}

	relay := telegramrelay.NewRelay(cfg.BotToken, cfg.ChatID)
	m := metrics.NewTelegramRelayMetrics()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /alert", telegramrelay.NewAlertHandler(relay, m, logger))
	// Not wrapped in any auth/CORS middleware — a Prometheus scraper carries
	// no JWT and is never called from a browser, matching every other
	// binary's own GET /metrics precedent.
	mux.Handle("GET /metrics", m.Handler())

	srv := &http.Server{Addr: cfg.ListenAddr, Handler: mux}

	go func() {
		logger.Info("telegram-relay listening", slog.String("addr", cfg.ListenAddr))
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("http shutdown did not complete cleanly", slog.Any("error", err))
	} else {
		logger.Info("shutdown complete")
	}
}
