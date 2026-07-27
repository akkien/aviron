package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"strings"

	"github.com/akkien/aviron/internal/config"
	"github.com/akkien/aviron/internal/consumer"
	"github.com/akkien/aviron/internal/db"
	"github.com/akkien/aviron/internal/kafka"
	"github.com/akkien/aviron/internal/postgres"
)

func Run(cfg *config.Config) {
	ctx := context.Background()

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
