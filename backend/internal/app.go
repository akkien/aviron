package internal

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"

	"github.com/akkien/aviron/internal/config"
	"github.com/akkien/aviron/internal/db"
	"github.com/akkien/aviron/internal/httpserver"
	"github.com/akkien/aviron/internal/metrics"
	"github.com/akkien/aviron/internal/middleware"
	"github.com/akkien/aviron/internal/room"
)

func Run(cfg *config.Config) {
	ctx := context.Background()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	pool, err := db.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(cfg.DatabaseURL); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	m := metrics.NewMetrics()
	registry := room.NewRegistry(logger, m)

	server := httpserver.NewServer()
	httpserver.RegisterRoutes(server, *cfg, pool, ctx, registry, logger, m)

	handler := middleware.RequestID()(middleware.RequestLog(logger)(middleware.Cors(cfg.CORSAllowedOrigin)(server)))

	logger.Info("listening", slog.String("port", cfg.Port))
	log.Fatal(http.ListenAndServe(":"+cfg.Port, handler))
}
