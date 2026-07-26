package internal

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

// RunConsumer is cmd/consumer's composition root, mirroring Run's shape
// for cmd/server. Lives here (package internal), not inside
// internal/consumer itself, for the same reason internal/httpserver/route.go
// is where internal/postgres and internal/room/internal/race get wired
// together rather than any one of those packages doing it themselves:
// internal/postgres's WorkoutSampleRepository/RaceRepository must import
// internal/consumer's interfaces/types, so internal/consumer can't import
// internal/postgres back without a cycle — composition has to happen one
// level up, where both are already visible.
func RunConsumer(cfg *config.Config) {
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
