package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/akkien/aviron/internal/config"
	"github.com/akkien/aviron/internal/consumer"
	"github.com/akkien/aviron/internal/db"
	"github.com/akkien/aviron/internal/kafka"
	"github.com/akkien/aviron/internal/postgres"
)

func Run(cfg *config.Config) {
	// internal/consumer's fetch loops already check ctx.Err() and flush
	// their in-flight batch before returning (graceful-shutdown.md) — the
	// only thing missing was a context that ever actually gets cancelled.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	brokers := strings.Split(cfg.KafkaBrokers, ",")
	producer := kafka.NewProducer(brokers, logger)
	defer producer.Close()

	writer := postgres.NewWorkoutSampleRepository(pool)
	reconciler := postgres.NewRaceRepository(pool)

	c := consumer.NewConsumer(brokers, writer, reconciler, producer, logger)

	logger.Info("consumer starting", slog.Any("brokers", brokers))
	if err := c.Run(ctx); err != nil {
		log.Fatalf("consumer: %v", err)
	}
}
